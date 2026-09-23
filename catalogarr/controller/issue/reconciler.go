/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package issue

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// mediaFileByIssueIndexKey indexes MediaFile by the Issue it backs,
	// filtered to spec.mediaRef.kind=issue -- the same shape as the episode
	// package's mediaFileByEpisodeIndexKey.
	mediaFileByIssueIndexKey = ".spec.mediaRef.issue"

	// downloadByIssueIndexKey indexes Download by the Issue its spec.target names
	// (kind issue only). It is how the reconciler finds the Downloads it
	// derives status.activeDownloadRef from (gap-fix ruling R-5), the same
	// shape as movie.downloadByMovieIndexKey. spec.target is immutable, so
	// the index never has to follow an edit.
	downloadByIssueIndexKey = ".spec.target.issue"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=comics,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not; see episode/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Issue: state from monitored/hasFile/downloading,
// the file/download rollup from a watched MediaFile and Download, and the
// cutoff decision against the owning Comic's QualityProfile. It is the sole
// writer of status.observedGeneration, status.conditions
// (Ready/HasFile/Released/CutoffMet), status.state, status.hasFile,
// status.fileRef, status.fileQuality, status.cutoffMet and
// status.activeDownloadRef; the provider-sourced fields (sourceID/title/date)
// belong to the Comic reconciler -- see this package's doc comment for the
// field-manager split.
//
// An Issue carries no QualityProfileRef of its own: it is ranked against its
// Comic's spec.qualityProfileRef (ComicSpec's "the QualityProfile issues are
// ranked against"), the way an Episode is ranked against its Series'.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder

	// Bus publishes this Issue's catalog item events (publishItem). Nil
	// publishes nothing, so a caller that wires no bus loses only history.
	Bus events.Publisher

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Issue controller: a predicate reacting to a
// spec change (GenerationChanged, e.g. a user editing spec.monitored) or to
// the Comic reconciler's own write of status.date (StatusFieldChanged) --
// server-side apply status writes by another manager (the Comic fan-out)
// bump no generation, so without this arm a newly-discovered or corrected
// cover date would sit unreflected in IssueConditionReleased/status.state
// until some unrelated event woke this controller -- and the MediaFile/
// Download watches, the same shape as episode/reconciler.go.
//
// Two watches keep status.cutoffMet current, since the profile it is
// decided against lives two objects away: an edited QualityProfile wakes
// every Issue of every Comic ranked against it (mapQualityProfile), and a
// Comic's own spec change -- pointing spec.qualityProfileRef at another
// profile -- wakes that Comic's Issues (mapComic).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByIssueIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindIssue {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &downloadv1alpha1.Download{}, downloadByIssueIndexKey,
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindIssue {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("issue").
		For(&catalogv1alpha1.Issue{}, builder.WithPredicates(issuePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.Comic{}, handler.EnqueueRequestsFromMapFunc(r.mapComic), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

func issuePredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok || iss.Status.Date == nil {
				return metav1.Time{}
			}
			return *iss.Status.Date
		}),
		// The grab worker's status.pendingGrab, under its own manager: a
		// delayed grab being recorded or consumed is a state change
		// (delayed <-> wanted) that bumps no generation.
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok || iss.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return iss.Status.PendingGrab.GrabAt
		}),
	)
}

// downloadPredicate is the same shape as the episode package's: a
// status-only phase transition never bumps generation, so GenerationChanged
// alone would never fire on it.
func downloadPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) downloadv1alpha1.DownloadPhase {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok {
				return ""
			}
			return dl.Status.Phase
		}),
		// A Download being torn down stops counting the moment it is
		// marked (rollup.DownloadNonTerminal), not when its finalizers let
		// it go.
		k8s.StatusFieldChanged(k8s.IsDeleting),
	)
}

func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindIssue {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload needs no List: a Download names its target in spec.target,
// so a Download that appears -- before anything has set the ref, which is
// the whole point of deriving the ref from the Download -- reaches its
// Issue directly.
func (r *Reconciler) mapDownload(_ context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok || dl.Spec.Target.Kind != commonv1.MediaKindIssue {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.Target.Name}}}
}

// activeDownload is the Download status.activeDownloadRef names, derived
// level-style (gap-fix ruling R-5, which makes this reconciler the field's
// only writer): the oldest Download targeting this Issue that it owns and
// that rollup.DownloadNonTerminal still counts, or nil. Ownership is by UID,
// so a Download left behind by a deleted Issue of the same name is never
// adopted.
func (r *Reconciler) activeDownload(ctx context.Context, iss *catalogv1alpha1.Issue) (*downloadv1alpha1.Download, error) {
	var list downloadv1alpha1.DownloadList
	if err := r.List(ctx, &list, client.InNamespace(iss.Namespace), client.MatchingFields{downloadByIssueIndexKey: iss.Name}); err != nil {
		return nil, err
	}
	return rollup.ActiveDownload(list.Items, func(d *downloadv1alpha1.Download) bool {
		return k8s.IsOwnedBy(d, iss)
	}), nil
}

// mapComic wakes every Issue of an edited Comic. It filters a namespaced
// List in Go rather than using the ".spec.comicRef" index: that index is
// registered by the COMIC controller (comic/reconciler.go's
// issueByComicRefIndexKey), and a second IndexField call for the same
// (type, field) on one manager cache is a hard "indexer conflict" error at
// startup -- the tradeoff episode.mapQualityProfile documents and makes for
// the identical reason. Comic spec edits are a cold path.
func (r *Reconciler) mapComic(ctx context.Context, o client.Object) []reconcile.Request {
	c, ok := o.(*catalogv1alpha1.Comic)
	if !ok {
		return nil
	}
	return r.issuesOf(ctx, c.Namespace, map[string]bool{c.Name: true})
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Issue ranked against it, two hops away: IssueSpec carries no
// QualityProfileRef, so this resolves profile -> Comics -> Issues.
// QualityProfile is cluster-scoped while Comic is namespaced, so the Comic
// List carries no client.InNamespace; both hops filter in Go (see mapComic
// for why no index), which is fine for a profile edited by hand.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var comics catalogv1alpha1.ComicList
	if err := r.List(ctx, &comics); err != nil {
		return nil
	}
	byNamespace := map[string]map[string]bool{}
	for _, c := range comics.Items {
		if c.Spec.QualityProfileRef != qp.Name {
			continue
		}
		if byNamespace[c.Namespace] == nil {
			byNamespace[c.Namespace] = map[string]bool{}
		}
		byNamespace[c.Namespace][c.Name] = true
	}
	var reqs []reconcile.Request
	for ns, names := range byNamespace {
		reqs = append(reqs, r.issuesOf(ctx, ns, names)...)
	}
	return reqs
}

// issuesOf lists the Issues in ns whose spec.comicRef is one of comics.
func (r *Reconciler) issuesOf(ctx context.Context, ns string, comics map[string]bool) []reconcile.Request {
	var issues catalogv1alpha1.IssueList
	if err := r.List(ctx, &issues, client.InNamespace(ns)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, iss := range issues.Items {
		if comics[iss.Spec.ComicRef] {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: iss.Namespace, Name: iss.Name}})
		}
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "issue.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var iss catalogv1alpha1.Issue
	if err := r.Get(ctx, req.NamespacedName, &iss); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&iss) {
		return r.reconcileDelete(ctx, &iss)
	}
	name, err := k8s.FinalizerFor(&iss, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &iss, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &iss)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, iss *catalogv1alpha1.Issue) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(iss, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(iss, name) {
		r.publishItem(ctx, iss, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, iss, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal computes hasFile from a watched MediaFile, folds in a
// watched Download's overlay, computes State and the HasFile/Released
// conditions, and patches status. It never sets
// SourceID/Title/Date -- those belong to the Comic reconciler.
func (r *Reconciler) reconcileNormal(ctx context.Context, iss *catalogv1alpha1.Issue) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(iss.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), iss.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(iss, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction).
	if action := rollup.ItemAction(iss.Generation, iss.Status.ObservedGeneration, iss.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, iss, action, now)
	}

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(iss.Namespace), client.MatchingFields{mediaFileByIssueIndexKey: iss.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	profile, profileProblem, err := r.resolveProfile(ctx, iss)
	if err != nil {
		return ctrl.Result{}, err
	}
	// fileFormatScore is discarded: IssueStatus has no leaf for it, and a
	// comic profile scores no custom formats anyway (pkg/quality.FromCRD
	// gives a non-video profile an empty Scores).
	hasFile, fileRef, fileQuality, _, cutoffMet := rollup.FileState(mf, profile)

	dl, err := r.activeDownload(ctx, iss)
	if err != nil {
		return ctrl.Result{}, err
	}
	_, active := rollup.DownloadOverlay(dl)

	state := State(monitored, hasFile, cutoffMet, active, iss.Status.PendingGrab != nil)

	released := iss.Status.Date != nil && !now.Before(iss.Status.Date.Time)
	k8s.MarkTrue(iss, &conditions, catalogv1alpha1.IssueConditionReleased, k8s.ReasonReconciled, "released=%t", released)
	if !released {
		k8s.MarkFalse(iss, &conditions, catalogv1alpha1.IssueConditionReleased, "Unreleased", "cover/store date has not passed yet")
	}
	if hasFile {
		k8s.MarkTrue(iss, &conditions, catalogv1alpha1.IssueConditionHasFile, "HasFile", "backed by MediaFile %s", ptr.Deref(fileRef, ""))
	} else {
		k8s.MarkFalse(iss, &conditions, catalogv1alpha1.IssueConditionHasFile, k8s.ReasonPending, "no MediaFile backs this issue")
	}
	status, reason, message := CutoffCondition(hasFile, cutoffMet, profileProblem)
	if status == metav1.ConditionTrue {
		k8s.MarkTrue(iss, &conditions, catalogv1alpha1.IssueConditionCutoffMet, reason, "%s", message)
	} else {
		k8s.MarkFalse(iss, &conditions, catalogv1alpha1.IssueConditionCutoffMet, reason, "%s", message)
	}

	// Ready is unconditionally true on a successful pass: unlike Movie/
	// Episode, IssueState has no phase value meaning "blocked, waiting on
	// something" (see state.go's doc comment) for Ready to gate on --
	// IssueConditionReady's own doc comment reads "True when the issue is
	// fully reconciled", not "True when acquired", and every branch above
	// that could fail already returned before reaching here.
	k8s.MarkReady(iss, &conditions, true, k8s.ReasonReconciled, "state=%s", state)

	statusAC := catalogac.IssueStatus().
		WithObservedGeneration(iss.Generation).
		WithState(state).
		WithHasFile(hasFile).
		WithCutoffMet(cutoffMet).
		WithConditions(k8s.ConditionACs(conditions)...)
	if fileRef != nil {
		statusAC = statusAC.WithFileRef(*fileRef)
	}
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if active {
		statusAC = statusAC.WithActiveDownloadRef(dl.Name)
	}
	// With no Download still working on this item, WithActiveDownloadRef is
	// deliberately not called: omitting a field this manager owns releases
	// it under SSA, and since R-5 this manager is its only owner, so the
	// release removes it.

	// This reconciler owns exactly State/Conditions/HasFile/FileRef/
	// FileQuality/CutoffMet/ActiveDownloadRef/ObservedGeneration under
	// k8s.ManagerCatalogarr. The Comic reconciler writes this Issue's
	// provider-sourced fields (SourceID/Title/Date) under the distinct
	// k8s.ManagerCatalogarrFanout, so no pass-through of those fields is
	// needed here -- see this package's doc.go.
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Issue(iss.Name, iss.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if iss.Status.Date != nil && now.Before(iss.Status.Date.Time) {
		return ctrl.Result{RequeueAfter: iss.Status.Date.Sub(now)}, nil
	}
	return ctrl.Result{}, nil
}

// CutoffCondition renders IssueConditionCutoffMet: True once the file backing
// the issue meets its Comic's profile cutoff, otherwise False with the
// reason it is not -- no file yet, a profile that could not be resolved
// (problem, which also keeps cutoffMet false rather than guessing), or a
// file below the cutoff, which leaves the issue an upgrade candidate.
func CutoffCondition(hasFile, cutoffMet bool, problem string) (status metav1.ConditionStatus, reason, message string) {
	switch {
	case !hasFile:
		return metav1.ConditionFalse, "NoFile", "no MediaFile backs this issue"
	case problem != "":
		return metav1.ConditionFalse, "ProfileUnresolved", "cutoff not evaluated: " + problem
	case cutoffMet:
		return metav1.ConditionTrue, "CutoffMet", "the file meets the quality profile's cutoff"
	default:
		return metav1.ConditionFalse, "BelowCutoff", "the file is below the quality profile's cutoff"
	}
}

// resolveProfile fetches the QualityProfile iss is ranked against: its
// Comic's spec.qualityProfileRef. Mirrors episode.Reconciler.resolveProfile's
// degrade-rather-than-fail contract: only a non-NotFound API error is fatal;
// a missing Comic, a missing profile or one that does not parse degrades to
// (nil, problem, nil), so the rest of the status still lands and cutoffMet
// reads false.
func (r *Reconciler) resolveProfile(ctx context.Context, iss *catalogv1alpha1.Issue) (*quality.Profile, string, error) {
	var c catalogv1alpha1.Comic
	if err := r.Get(ctx, types.NamespacedName{Namespace: iss.Namespace, Name: iss.Spec.ComicRef}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("comic %q not found", iss.Spec.ComicRef), nil
		}
		return nil, "", err
	}
	if c.Spec.QualityProfileRef == "" {
		return nil, fmt.Sprintf("comic %q names no qualityProfileRef", c.Name), nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Name: c.Spec.QualityProfileRef}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("qualityProfile %q not found", c.Spec.QualityProfileRef), nil
		}
		return nil, "", err
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) > 0 {
		return nil, fmt.Sprintf("qualityProfile %q does not parse: %s", qp.Name, errors.Join(errs...)), nil
	}
	return &p, "", nil
}
