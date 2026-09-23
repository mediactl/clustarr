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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// mediaFileByIssueIndexKey indexes MediaFile by the Issue it backs,
	// filtered to spec.mediaRef.kind=issue -- the same shape as the episode
	// package's mediaFileByEpisodeIndexKey.
	mediaFileByIssueIndexKey = ".spec.mediaRef.issue"

	// issueByActiveDownloadIndexKey indexes Issue by its
	// status.activeDownloadRef, the reverse direction from a watched
	// Download back to the Issue holding the reference.
	issueByActiveDownloadIndexKey = ".status.activeDownloadRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not; see episode/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Issue: state from monitored/hasFile/downloading,
// and the file/download rollup from a watched MediaFile and Download. It is
// the sole writer of status.observedGeneration, status.conditions
// (Ready/HasFile/Released), status.state, status.hasFile, status.fileRef,
// status.fileQuality and status.activeDownloadRef; the provider-sourced
// fields (sourceID/title/date) belong to the Comic reconciler -- see this
// package's doc comment for the field-manager split.
//
// Unlike Episode, this reconciler resolves no QualityProfile: IssueStatus
// has no CutoffMet/FileFormatScore leaf to record a ranking outcome in (see
// doc.go), so there is nothing here for a profile lookup to feed. Like
// Episode, it carries no Bus field -- Issue has no metadata of its own to
// refresh; the Comic reconciler's issue-listing RPC is what keeps its
// provider fields current.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

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
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Issue{}, issueByActiveDownloadIndexKey,
		func(o client.Object) []string {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok || iss.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*iss.Status.ActiveDownloadRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("issue").
		For(&catalogv1alpha1.Issue{}, builder.WithPredicates(issuePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

func issuePredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok || iss.Status.Date == nil {
				return metav1.Time{}
			}
			return *iss.Status.Date
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
	)
}

func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindIssue {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

func (r *Reconciler) mapDownload(ctx context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return nil
	}
	var issues catalogv1alpha1.IssueList
	if err := r.List(ctx, &issues, client.InNamespace(dl.Namespace), client.MatchingFields{issueByActiveDownloadIndexKey: dl.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(issues.Items))
	for _, iss := range issues.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: iss.Namespace, Name: iss.Name}})
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

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(iss.Namespace), client.MatchingFields{mediaFileByIssueIndexKey: iss.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	// profile is deliberately nil: IssueStatus has no CutoffMet or
	// FileFormatScore field to hold a ranking decision in (this package's
	// doc.go), so there is no QualityProfile to resolve. rollup.FileState
	// still derives hasFile/fileRef/fileQuality correctly with a nil
	// profile (its own doc comment); only its last two return values
	// (fileFormatScore, cutoffMet), which this package has nowhere to put,
	// are discarded.
	hasFile, fileRef, fileQuality, _, _ := rollup.FileState(mf, nil)

	var dl *downloadv1alpha1.Download
	if iss.Status.ActiveDownloadRef != nil {
		var d downloadv1alpha1.Download
		if err := r.Get(ctx, types.NamespacedName{Namespace: iss.Namespace, Name: *iss.Status.ActiveDownloadRef}, &d); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else {
			dl = &d
		}
	}
	_, active := rollup.DownloadOverlay(dl)

	state := State(monitored, hasFile, active)

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
		WithConditions(k8s.ConditionACs(conditions)...)
	if fileRef != nil {
		statusAC = statusAC.WithFileRef(*fileRef)
	}
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if active && iss.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*iss.Status.ActiveDownloadRef)
	}
	// When !active and a ref was set, WithActiveDownloadRef is deliberately
	// not called -- clears it by omission, the same documented convention as
	// movie/episode's identical clearing (no comic grab worker exists yet to
	// co-own this field under a distinct manager, so today nothing ever sets
	// it in the first place; this keeps the reconciler correct for when one
	// does, per the brief's "the pair that most closely mirrors
	// Series->Episode").

	// This reconciler owns exactly State/Conditions/HasFile/FileRef/
	// FileQuality/ActiveDownloadRef/ObservedGeneration under
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
