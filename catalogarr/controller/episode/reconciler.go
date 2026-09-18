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

package episode

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// mediaFileByEpisodeIndexKey indexes MediaFile by the Episode it backs,
	// filtered to spec.mediaRef.kind=episode -- the same shape as the
	// movie package's mediaFileByMovieIndexKey.
	mediaFileByEpisodeIndexKey = ".spec.mediaRef.episode"

	// episodeByActiveDownloadIndexKey indexes Episode by its
	// status.activeDownloadRef, the reverse direction from a watched
	// Download back to the Episode holding the reference.
	episodeByActiveDownloadIndexKey = ".status.activeDownloadRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Episode: phase from monitored/airDate/hasFile/
// cutoffMet, and the file/download rollup from a watched MediaFile and
// Download. It is the sole writer of status.phase, status.hasFile,
// status.fileRef, status.fileQuality, status.fileFormatScore,
// status.cutoffMet and status.activeDownloadRef; the provider-sourced
// fields (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes)
// belong to the Series reconciler -- see this package's doc comment for the
// field-manager split.
//
// Unlike Movie and Series, this reconciler does no publishing (Episode has
// no metadata of its own to refresh -- the Series reconciler's episode-list
// RPC is what keeps its provider fields current), so it carries no Bus
// field.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Episode controller: a predicate reacting
// to a spec change (GenerationChanged, e.g. a user editing spec.monitored)
// or to the Series reconciler's own write of status.airDate
// (StatusFieldChanged) -- a newly-discovered air date must wake this
// controller immediately, not wait for the next RequeueAfter poll -- and
// the MediaFile/Download watches added in review.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByEpisodeIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindEpisode {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Episode{}, episodeByActiveDownloadIndexKey,
		func(o client.Object) []string {
			ep, ok := o.(*catalogv1alpha1.Episode)
			if !ok || ep.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*ep.Status.ActiveDownloadRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("episode").
		For(&catalogv1alpha1.Episode{}, builder.WithPredicates(episodePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

func episodePredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			ep, ok := o.(*catalogv1alpha1.Episode)
			if !ok || ep.Status.AirDate == nil {
				return metav1.Time{}
			}
			return *ep.Status.AirDate
		}),
	)
}

// downloadPredicate is the same shape as the movie package's: a status-only
// phase transition never bumps generation, so GenerationChanged alone would
// never fire on it.
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
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindEpisode {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

func (r *Reconciler) mapDownload(ctx context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return nil
	}
	var episodes catalogv1alpha1.EpisodeList
	if err := r.List(ctx, &episodes, client.InNamespace(dl.Namespace), client.MatchingFields{episodeByActiveDownloadIndexKey: dl.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(episodes.Items))
	for _, ep := range episodes.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}})
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var ep catalogv1alpha1.Episode
	if err := r.Get(ctx, req.NamespacedName, &ep); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&ep) {
		return r.reconcileDelete(ctx, &ep)
	}
	name, err := k8s.FinalizerFor(&ep, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &ep, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &ep)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, ep *catalogv1alpha1.Episode) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(ep, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, ep, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal computes hasFile/cutoffMet from a watched MediaFile
// (resolving the QualityProfile via the owning Series, since EpisodeSpec has
// no QualityProfileRef of its own -- episodes are ranked against the
// Series's profile), folds in a watched Download's overlay, computes Phase
// and the Aired condition, and patches status. It never sets
// Title/Overview/AirDate/TvdbID/AbsoluteNumber/RuntimeMinutes -- those
// belong to the Series reconciler.
func (r *Reconciler) reconcileNormal(ctx context.Context, ep *catalogv1alpha1.Episode) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(ep.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), ep.Status.Conditions...)

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(ep.Namespace), client.MatchingFields{mediaFileByEpisodeIndexKey: ep.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := pickMediaFile(mfList.Items)

	profile, err := r.resolveProfile(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	hasFile, fileRef, fileQuality, fileFormatScore, cutoffMet := FileState(mf, profile)

	var dl *downloadv1alpha1.Download
	if ep.Status.ActiveDownloadRef != nil {
		var d downloadv1alpha1.Download
		if err := r.Get(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: *ep.Status.ActiveDownloadRef}, &d); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else {
			dl = &d
		}
	}
	overlayPhase, active := DownloadOverlay(dl)

	phase := Phase(monitored, ep.Status.AirDate, hasFile, cutoffMet, now)
	if overlayPhase != "" {
		phase = overlayPhase
	}

	aired := ep.Status.AirDate != nil && !now.Before(ep.Status.AirDate.Time)
	k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionAired, k8s.ReasonReconciled, "aired=%t", aired)
	if !aired {
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionAired, "Unaired", "air date has not passed yet")
	}
	k8s.MarkReady(ep, &conditions, phase != catalogv1alpha1.EpisodePhaseUnaired || ep.Status.AirDate == nil, k8s.ReasonReconciled, "phase=%s", phase)

	statusAC := catalogac.EpisodeStatus().
		WithObservedGeneration(ep.Generation).
		WithPhase(phase).
		WithHasFile(hasFile).
		WithFileFormatScore(fileFormatScore).
		WithCutoffMet(cutoffMet).
		WithConditions(k8s.ConditionACs(conditions)...)
	if fileRef != nil {
		statusAC = statusAC.WithFileRef(*fileRef)
	}
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if active && ep.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*ep.Status.ActiveDownloadRef)
	}
	// When !active and a ref was set, WithActiveDownloadRef is deliberately
	// not called -- see the identical rationale in the movie package's
	// reconciler.go.

	// Pass through the Series reconciler's own fields verbatim (same field
	// manager, k8s.ManagerCatalogarr, disjoint concerns). This is load-
	// bearing, not decorative: server-side apply tracks one field set PER
	// MANAGER NAME, not per call site, so an apply that omits a field this
	// manager previously sent RELEASES it (proven empirically against a
	// real envtest apiserver during this task's development, the same
	// mechanism pkg/k8s/patch_envtest_test.go's
	// TestPatchStatusReleasesItsOwnFieldsOnly demonstrates for a single
	// writer). Without this, this reconciler's own patch would silently
	// wipe the title/overview/airDate/etc the Series reconciler just wrote
	// on its next pass, and vice versa. Conditions is exempt: it is a
	// +listType=map keyed by type, and SSA merges list entries by that key
	// regardless of which manager sent which entry, so it does not need
	// re-asserting here.
	if ep.Status.TvdbID != 0 {
		statusAC = statusAC.WithTvdbID(ep.Status.TvdbID)
	}
	if ep.Status.Title != "" {
		statusAC = statusAC.WithTitle(ep.Status.Title)
	}
	if ep.Status.Overview != "" {
		statusAC = statusAC.WithOverview(ep.Status.Overview)
	}
	if ep.Status.AirDate != nil {
		statusAC = statusAC.WithAirDate(*ep.Status.AirDate)
	}
	if ep.Status.RuntimeMinutes != 0 {
		statusAC = statusAC.WithRuntimeMinutes(ep.Status.RuntimeMinutes)
	}
	if ep.Status.AbsoluteNumber != nil {
		statusAC = statusAC.WithAbsoluteNumber(*ep.Status.AbsoluteNumber)
	}
	if ep.Status.FinaleType != "" {
		statusAC = statusAC.WithFinaleType(ep.Status.FinaleType)
	}

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Episode(ep.Name, ep.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if ep.Status.AirDate != nil && now.Before(ep.Status.AirDate.Time) {
		return ctrl.Result{RequeueAfter: ep.Status.AirDate.Sub(now)}, nil
	}
	return ctrl.Result{}, nil
}

// resolveProfile fetches the QualityProfile episodes are ranked against via
// the owning Series (ep.Spec.SeriesRef -> Series.Spec.QualityProfileRef),
// since EpisodeSpec carries no QualityProfileRef of its own. A missing
// Series or QualityProfile degrades to a nil profile (FileState then
// reports cutoffMet=false) rather than failing the whole reconcile: an
// Episode is only ever created by its owning Series, but that Series could
// be deleted (finalizer permitting) or its profile reference could be
// stale.
func (r *Reconciler) resolveProfile(ctx context.Context, ep *catalogv1alpha1.Episode) (*quality.Profile, error) {
	var s catalogv1alpha1.Series
	if err := r.Get(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: ep.Spec.SeriesRef}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if s.Spec.QualityProfileRef == "" {
		return nil, nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: s.Spec.QualityProfileRef}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) != 0 {
		return nil, nil
	}
	return &p, nil
}

// pickMediaFile chooses the MediaFile an Episode's status should reflect:
// the one flagged Original, else the most recently created, else nil. Same
// shape as the movie package's own pickMediaFile -- small enough (and
// reconciler-specific enough, unlike FileState/DownloadOverlay) that the C6
// controller amendment does not name it as one to share via rollup.
func pickMediaFile(items []catalogv1alpha1.MediaFile) *catalogv1alpha1.MediaFile {
	if len(items) == 0 {
		return nil
	}
	for i := range items {
		if ptr.Deref(items[i].Spec.Original, false) {
			return &items[i]
		}
	}
	best := &items[0]
	for i := 1; i < len(items); i++ {
		if items[i].CreationTimestamp.After(best.CreationTimestamp.Time) {
			best = &items[i]
		}
	}
	return best
}
