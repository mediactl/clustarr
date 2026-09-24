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

package audiobook

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
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// mediaFileByAudiobookIndexKey indexes MediaFile by the Audiobook it
	// backs, filtered to spec.mediaRef.kind=audiobook so another kind's
	// MediaFile (same name is not possible across kinds today, but this
	// future-proofs the index against that) never matches an Audiobook's
	// List. Mirrors movie's mediaFileByMovieIndexKey.
	mediaFileByAudiobookIndexKey = ".spec.mediaRef.audiobook"

	// downloadByAudiobookIndexKey indexes Download by the Audiobook its spec.target names
	// (kind audiobook only). It is how the reconciler finds the Downloads it
	// derives status.activeDownloadRef from (gap-fix ruling R-5), the same
	// shape as movie.downloadByMovieIndexKey. spec.target is immutable, so
	// the index never has to follow an edit.
	downloadByAudiobookIndexKey = ".spec.target.audiobook"

	// audiobookByQualityProfileIndexKey indexes Audiobook by the
	// QualityProfile it is ranked against, so a watched QualityProfile can
	// be mapped back to every Audiobook whose cutoffMet depends on it.
	audiobookByQualityProfileIndexKey = ".spec.qualityProfileRef"

	// audiobookByBookRefIndexKey indexes Audiobook by spec.bookRef, the
	// reverse direction from a watched Book back to every Audiobook linking
	// to it -- see bookref.go's mapBookRef.
	audiobookByBookRefIndexKey = ".spec.bookRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. See movie's identical marker
// and reconciler.go comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Audiobook: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path, the
// spec.bookRef link check, and the file/download rollup from a watched
// MediaFile and Download. It is the sole writer of status.phase,
// status.path, status.hasFile, status.fileRefs, status.quality,
// status.cutoffMet and status.activeDownloadRef (§3's single-writer rule);
// status.metadata belongs to the metadata gateway (field manager
// k8s.ManagerCatalogarrMetadata) and this reconciler never builds an
// AudiobookStatusApplyConfiguration that calls WithMetadata.
//
// status.activeDownloadRef is this reconciler's alone too (gap-fix ruling
// R-5): it is derived level-style from the Audiobook's own non-terminal
// Downloads (activeDownload), exactly as movie.Reconciler derives a Movie's,
// and the grab path no longer writes it. status.pendingGrab is read-only
// here, for Phase and the wake predicate below, and is never written by
// this reconciler.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      events.Publisher

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Audiobook controller: the finalizer/
// metadata-refresh predicate on Audiobook itself, and the MediaFile/
// Download/QualityProfile/Book watches so an imported file, an active
// download's phase change, a profile edit or a Book appearing after its
// Audiobook all reach this reconciler without waiting for a poll.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByAudiobookIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAudiobook {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &downloadv1alpha1.Download{}, downloadByAudiobookIndexKey,
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindAudiobook {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Audiobook{}, audiobookByQualityProfileIndexKey,
		func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{a.Spec.QualityProfileRef}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Audiobook{}, audiobookByBookRefIndexKey,
		func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.BookRef == nil || *a.Spec.BookRef == "" {
				return nil
			}
			return []string{*a.Spec.BookRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("audiobook").
		For(&catalogv1alpha1.Audiobook{}, builder.WithPredicates(audiobookPredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.Book{}, handler.EnqueueRequestsFromMapFunc(r.mapBookRef), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// audiobookPredicate wakes this controller on a spec change
// (GenerationChanged), on the metadata gateway's own write
// (StatusFieldChanged scoped to status.metadata.refreshedAt) or on the grab
// path's status.pendingGrab -- and nothing else, so this controller's own
// Phase/Conditions/Path/file-rollup patch, which touches none of those,
// does not loop it. Mirrors movie.moviePredicate exactly; see its doc
// comment for why the pendingGrab arm is what makes Phase=Delayed reachable
// at all.
func audiobookPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Status.Metadata == nil {
				return metav1.Time{}
			}
			return a.Status.Metadata.RefreshedAt
		}),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return a.Status.PendingGrab.GrabAt
		}),
	)
}

// downloadPredicate wakes the Download watch on a spec change (Create
// always passes regardless) or on a status.phase transition. Duplicated
// from movie.downloadPredicate rather than imported: it is unexported
// there, and the logic is three lines, cheaper to repeat once per kind (as
// episode's own copy already does) than to lift into a shared package for a
// single call site each.
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

// mapMediaFile needs no List/index -- a MediaFile already carries its
// target's identity directly in spec.mediaRef, so this is the cheap
// direction.
func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAudiobook {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload needs no List: a Download names its target in spec.target,
// so a Download that appears -- before anything has set the ref, which is
// the whole point of deriving the ref from the Download -- reaches its
// Audiobook directly.
func (r *Reconciler) mapDownload(_ context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok || dl.Spec.Target.Kind != commonv1.MediaKindAudiobook {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.Target.Name}}}
}

// activeDownload is the Download status.activeDownloadRef names, derived
// level-style (gap-fix ruling R-5, which makes this reconciler the field's
// only writer): the oldest Download targeting this Audiobook that it owns and
// that rollup.DownloadNonTerminal still counts, or nil. Ownership is by UID,
// so a Download left behind by a deleted Audiobook of the same name is never
// adopted.
func (r *Reconciler) activeDownload(ctx context.Context, m *catalogv1alpha1.Audiobook) (*downloadv1alpha1.Download, error) {
	var list downloadv1alpha1.DownloadList
	if err := r.List(ctx, &list, client.InNamespace(m.Namespace), client.MatchingFields{downloadByAudiobookIndexKey: m.Name}); err != nil {
		return nil, err
	}
	return rollup.ActiveDownload(list.Items, func(d *downloadv1alpha1.Download) bool {
		return k8s.IsOwnedBy(d, m)
	}), nil
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Audiobook ranked against it. QualityProfile is CLUSTER-scoped
// while Audiobook is namespaced, so the List deliberately carries no
// client.InNamespace: one profile is shared by every namespace, and
// enqueueing only one of them would be arbitrary. Mirrors
// movie.mapQualityProfile exactly.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var audiobooks catalogv1alpha1.AudiobookList
	if err := r.List(ctx, &audiobooks, client.MatchingFields{audiobookByQualityProfileIndexKey: qp.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(audiobooks.Items))
	for _, a := range audiobooks.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return (the rest of this reconcile runs against
// the same in-memory object in the same pass), then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "audiobook.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Audiobook
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&a) {
		return r.reconcileDelete(ctx, &a)
	}
	name, err := k8s.FinalizerFor(&a, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &a, name); err != nil {
		return ctrl.Result{}, err
	}
	// status.artwork drifted from its sources: ask the metadata gateway to
	// re-fetch (spec §B.7). Before any status apply, so a failed publish
	// returns without writing and the requeue retries it.
	if err := artwork.PublishFetch(ctx, r.Bus, &a, commonv1.MediaKindAudiobook); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &a)
}

// reconcileDelete removes the finalizer. There is nothing else owned outside
// Kubernetes at this phase, mirroring movie.reconcileDelete's reasoning
// exactly: the mediaKey convention this task defines (namespace/name, no KV
// state of its own) has no clustarr-leases or clustarr-pending entries
// created by this controller to clean up.
func (r *Reconciler) reconcileDelete(ctx context.Context, m *catalogv1alpha1.Audiobook) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(m, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(m, name) {
		r.publishItem(ctx, m, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, m, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal implements spec §8.1's Want flow for Audiobook: the
// bookRef link check (always run, never blocking), metadata staleness
// decided and a MetadataTask published when needed, path computed from
// whatever metadata is cached, and the MediaFile/Download rollup folded in.
func (r *Reconciler) reconcileNormal(ctx context.Context, m *catalogv1alpha1.Audiobook) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(m.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), m.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(m, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction).
	if action := rollup.ItemAction(m.Generation, m.Status.ObservedGeneration, m.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, m, action, now)
	}

	statusAC := catalogac.AudiobookStatus().WithObservedGeneration(m.Generation)

	// The bookRef check runs unconditionally, before either early-return
	// path below, so its result is folded into `conditions` (and therefore
	// into reassertKnownStatus's WithConditions call) regardless of which
	// return this reconcile takes. It never fails the reconcile -- see
	// resolveBookRef's doc comment.
	bookResolved, err := r.resolveBookRef(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case m.Spec.BookRef == nil || *m.Spec.BookRef == "":
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, k8s.ReasonReconciled, "no bookRef set")
	case bookResolved:
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, k8s.ReasonReconciled, "book %q found", *m.Spec.BookRef)
	default:
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, "BookRefNotFound", "book %q not found", *m.Spec.BookRef)
	}

	stale := m.Status.Metadata == nil
	if !stale {
		// MediaKindAudiobook is a flat 30-day cadence regardless of state
		// (pkg/metadata/refresh.go), so RefreshStateActive is passed only to
		// avoid metadata.RefreshTTL's two magic-string special cases ahead of
		// its per-kind switch -- exactly the reasoning
		// app/catalog/metadata/worker.go's own Audiobook branch comment gives.
		ttl := metadata.RefreshTTL(commonv1.MediaKindAudiobook, metadata.RefreshStateActive, m.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(m.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		// The envelope key is the <namespace>/<name> routing key every
		// worker parses to recover the namespace; the media key is the
		// subject token. They are not interchangeable -- the media key is
		// tokenised for the wire and has no slash to cut on. This is also
		// the entire "does region reach the metadata task" story: the task
		// below carries only MediaRef{Kind, Name} (schema.MetadataTask has
		// no region field), and the metadata gateway's Handler re-Gets this
		// exact object by that name before it ever looks at region
		// (app/catalog/metadata/worker.go's Handle, target.go's
		// externalIDs) -- so region reaches the gateway through
		// m.Spec.Region on the live object, not through anything this
		// reconciler puts on the wire. This reconciler's only job is to ask
		// for a refresh of the right object.
		envKey := m.Namespace + "/" + m.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindAudiobook), m.Namespace, m.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: m.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(m.UID), m.Generation, "metadata"),
			Type:   "catalog.MetadataTask",
			Schema: schemaName,
			Source: "catalogarr@" + version.String(),
			Key:    envKey,
			Time:   now,
			Data:   data,
		}
		_, pubErr := r.Bus.Publish(ctx, events.WorkMetadataSubject(events.PriorityNormal, mediaKey), env)
		if pubErr != nil {
			if errors.Is(pubErr, events.ErrQueueFull) {
				k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, m)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "Published", "no refresh currently queued")
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if m.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(m, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", m.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, m)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(statusAC)); perr != nil {
					return ctrl.Result{}, perr
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, err
		}

		eng := naming.NewEngine(naming.Config{
			Dialect:           naming.Dialect(rf.Spec.Naming.Dialect),
			ColonReplacement:  naming.ColonReplacement(rf.Spec.Naming.ColonReplacement),
			MultiEpisodeStyle: naming.MultiEpisodeStyle(rf.Spec.Naming.MultiEpisodeStyle),
			Overrides:         rf.Spec.Naming.Overrides,
		})
		nctx := namingContext(m.Spec, m.Status.Metadata)

		pth, err := Path(rf.Spec.Path, m.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(m.Namespace), client.MatchingFields{mediaFileByAudiobookIndexKey: m.Name}); err != nil {
		return ctrl.Result{}, err
	}

	profile, profileProblem, err := r.resolveProfile(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"audiobook", m.Name, "namespace", m.Namespace,
			"qualityProfileRef", m.Spec.QualityProfileRef, "problem", profileProblem)
	}
	hasFile, fileRefs, fileQuality, cutoffMet := FileState(mfList.Items, profile)
	// Announced before the apply that records the new fileRefs, so a failed
	// apply re-announces the same edges under the same envelope ids
	// (rollup.MediaFileEvent) rather than losing them.
	for _, e := range FileTransitions(m.Status.FileRefs, fileRefs, mfList.Items) {
		r.publishFile(ctx, m, e.Action, e.File, e.MediaFile, now)
	}

	dl, err := r.activeDownload(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	overlayPhase, active := DownloadOverlay(dl)

	phase := Phase(monitored, hasFile, cutoffMet, m.Status.PendingGrab != nil)
	if overlayPhase != "" {
		phase = overlayPhase
	}

	statusAC = statusAC.
		WithPhase(phase).
		WithHasFile(hasFile).
		WithCutoffMet(cutoffMet).
		WithFileRefs(fileRefs...)
	if fileQuality != nil {
		statusAC = statusAC.WithQuality(*fileQuality)
	}
	if active {
		statusAC = statusAC.WithActiveDownloadRef(dl.Name)
	}
	// With no Download still working on this item, WithActiveDownloadRef is
	// deliberately not called: omitting a field this manager owns releases
	// it under SSA, and since R-5 this manager is its only owner, so the
	// release removes it.

	if hasFile {
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionHasFile, "HasFile", "backed by %d MediaFile(s)", len(fileRefs))
	} else {
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionHasFile, k8s.ReasonPending, "no MediaFile backs this audiobook")
	}
	switch {
	case !hasFile:
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionCutoffMet, k8s.ReasonPending, "no file to rank against the profile cutoff")
	case profile == nil:
		// See movie.reconcileNormal's CutoffMet-condition comment for why
		// this is a distinct reason rather than the ordinary CutoffUnmet:
		// reporting CutoffUnmet here would claim a verdict that was never
		// reached, silently parking the item in the search rotation looking
		// like a legitimate upgrade candidate.
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionCutoffMet, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
	case cutoffMet:
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionCutoffMet, "CutoffMet", "files meet the profile cutoff")
	default:
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionCutoffMet, "CutoffUnmet", "files do not meet the profile cutoff")
	}

	// Ready gates on metaReady alone -- there is no `phase != Pending` term
	// the way movie.reconcileNormal has one, because AudiobookPhase has no
	// Pending value to compare against (see Phase's doc comment). HasFile,
	// CutoffMet and BookRefResolved are independent siblings, exactly as
	// Movie's Available/HasFile/CutoffMet do not gate Ready either.
	k8s.MarkReady(m, &conditions, metaReady, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// resolveProfile fetches the QualityProfile this Audiobook is ranked
// against and reports, as a non-empty problem string, any reason it could
// not be resolved: no reference set, the reference does not resolve, or the
// profile exists but does not parse against the catalogue. Verbatim
// movie.resolveProfile, adapted to Audiobook's own type -- see its doc
// comment for why only a non-NotFound API error is fatal.
//
// pkg/quality's built-in "audiobook" profile (mediaKind audiobook, cutoff
// MP3, catalogue/data/profiles/audiobook.json) is seeded by the
// qualityprofile controller's Bootstrap the same as every other built-in,
// and pkg/quality/definition.go's nonVideoDefinitions carries a real
// "audiobook" ladder (Unknown Audio < MP3 < M4B < FLAC, design §9) --
// quality.FromCRD resolves an audiobook profile exactly as it resolves a
// video one. Per R3 (docs/superpowers/plans/2026-09-23-phase-g-parity.md),
// automated search/grab for a kind is in scope only where pkg/quality
// already supports it; this reconciler's cutoff evaluation is that support
// for Audiobook, so it is wired here rather than skipped.
func (r *Reconciler) resolveProfile(ctx context.Context, m *catalogv1alpha1.Audiobook) (*quality.Profile, string, error) {
	if m.Spec.QualityProfileRef == "" {
		return nil, "spec.qualityProfileRef is empty", nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Name: m.Spec.QualityProfileRef}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("qualityProfile %q not found", m.Spec.QualityProfileRef), nil
		}
		return nil, "", err
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) > 0 {
		return nil, fmt.Sprintf("qualityProfile %q does not parse: %s", qp.Name, errors.Join(errs...)), nil
	}
	return &p, "", nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/Conditions to statusAC, sourced from m's current
// (pre-reconcile) status. Used on the two early-return paths (QueueFull,
// RootFolderNotFound): both are transient failures, and without this,
// PatchStatus's apply would omit every field it does not mention, releasing
// (zeroing) a healthy Audiobook's Phase/Path/HasFile/FileRefs/Quality/
// CutoffMet/ActiveDownloadRef the next time either blip happens. Verbatim
// movie.reassertKnownStatus's reasoning, adapted to Audiobook's field set
// (no Available/AvailableAt/FileFormatScore; FileRefs is a list, reasserted
// via WithFileRefs(m.Status.FileRefs...) rather than one optional scalar --
// see filestate.go's doc comment on why that call is safe to make
// unconditionally: an empty slice spreads to zero args and the field stays
// released, exactly the outcome wanted when there is genuinely no file).
func reassertKnownStatus(statusAC *catalogac.AudiobookStatusApplyConfiguration, m *catalogv1alpha1.Audiobook) *catalogac.AudiobookStatusApplyConfiguration {
	if m.Status.Phase != "" {
		statusAC = statusAC.WithPhase(m.Status.Phase)
	}
	if m.Status.Path != "" {
		statusAC = statusAC.WithPath(m.Status.Path)
	}
	statusAC = statusAC.WithHasFile(m.Status.HasFile)
	statusAC = statusAC.WithFileRefs(m.Status.FileRefs...)
	if m.Status.Quality != nil {
		statusAC = statusAC.WithQuality(*m.Status.Quality)
	}
	statusAC = statusAC.WithCutoffMet(m.Status.CutoffMet)
	if m.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*m.Status.ActiveDownloadRef)
	}
	return statusAC
}
