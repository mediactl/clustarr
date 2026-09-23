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
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// mediaFileByEpisodeIndexKey indexes MediaFile by the Episode it backs,
	// filtered to spec.mediaRef.kind=episode -- the same shape as the
	// movie package's mediaFileByMovieIndexKey.
	mediaFileByEpisodeIndexKey = ".spec.mediaRef.episode"

	// downloadByEpisodeIndexKey indexes Download by every Episode its
	// spec.target covers: the episode itself for a single-episode grab
	// (kind episode), and each of spec.target.keys for a season pack (kind
	// series), whose keys are Episode names. It is how the reconciler finds
	// the Downloads it derives status.activeDownloadRef from. spec.target is
	// immutable, so the index never has to follow an edit.
	downloadByEpisodeIndexKey = ".spec.target.episode"

	// seriesByQualityProfileIndexKey indexes SERIES, not Episode, by the
	// QualityProfile it is ranked against: an Episode carries no
	// QualityProfileRef of its own, so the reverse hop from an edited
	// profile runs profile -> Series -> Episodes.
	seriesByQualityProfileIndexKey = ".spec.qualityProfileRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
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
// status.activeDownloadRef has had exactly one writer since gap-fix ruling
// R-5: this reconciler, deriving it from the non-terminal Downloads that
// cover the Episode -- its own single-episode grabs, and the season packs
// its Series owns whose spec.target.keys name it. See the movie package's
// Reconciler doc for the history.
//
// Unlike Movie and Series, this reconciler publishes no metadata work
// (Episode has no metadata of its own to refresh -- the Series reconciler's
// episode-list RPC is what keeps its provider fields current). Bus is used
// only for the media-file domain events (imported/replaced/deleted) the
// history sink records; a nil Bus publishes nothing, which is how a
// catalogarr that has not wired one behaves.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      events.Publisher

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// RegisterIndexes registers every field index Reconcile's List calls and
// the watches' map functions read, on idx. SetupWithManager calls it; a test
// that drives Reconcile against a bare manager cache calls it too, so the
// index names and extractors live in exactly one place.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, mediaFileByEpisodeIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindEpisode {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, downloadByEpisodeIndexKey,
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok {
				return nil
			}
			return coveredEpisodes(dl.Spec.Target)
		}); err != nil {
		return err
	}
	return idx.IndexField(ctx, &catalogv1alpha1.Series{}, seriesByQualityProfileIndexKey,
		func(o client.Object) []string {
			s, ok := o.(*catalogv1alpha1.Series)
			if !ok || s.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{s.Spec.QualityProfileRef}
		})
}

// coveredEpisodes names the Episodes a Download's target covers: the one
// Episode of a single-episode grab, the keys of a season pack (the grab
// path's StatusTargets expands a Series target the same way), nothing for
// any other kind. A Series target with no keys covers no Episode that can
// be named, so it names none rather than guessing at the whole series.
func coveredEpisodes(target commonv1.MediaRef) []string {
	switch target.Kind {
	case commonv1.MediaKindEpisode:
		return []string{target.Name}
	case commonv1.MediaKindSeries:
		return target.Keys
	default:
		return nil
	}
}

// SetupWithManager registers the Episode controller: a predicate reacting
// to a spec change (GenerationChanged, e.g. a user editing spec.monitored)
// or to the Series reconciler's own write of status.airDate
// (StatusFieldChanged) -- a newly-discovered air date must wake this
// controller immediately, not wait for the next RequeueAfter poll -- and
// the MediaFile/Download watches added in review.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("episode").
		For(&catalogv1alpha1.Episode{}, builder.WithPredicates(episodePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
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
		// The grab worker's status.pendingGrab write bumps no generation and
		// touches no air date, so without this arm Phase=Delayed would never
		// be recomputed and the episode would sit at Wanted for the whole
		// delay window. grabAt is the extracted key because it changes
		// whenever the pending grab is set, rescheduled or cleared.
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			ep, ok := o.(*catalogv1alpha1.Episode)
			if !ok || ep.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return ep.Status.PendingGrab.GrabAt
		}),
		// The DLQ projector's annotation changes neither generation nor
		// status; see the movie package's moviePredicate.
		k8s.DeadLetteredAnnotationChanged(),
	)
}

// downloadPredicate is the same shape as the movie package's: a status-only
// phase transition never bumps generation, so GenerationChanged alone would
// never fire on it, and a Download being deleted stops counting the moment
// it is marked.
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
		k8s.StatusFieldChanged(k8s.IsDeleting),
	)
}

func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindEpisode {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload needs no List: a Download names what it covers in
// spec.target, so a new Download -- single episode or season pack --
// reaches every Episode it covers directly, before anything has set a ref.
func (r *Reconciler) mapDownload(_ context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return nil
	}
	names := coveredEpisodes(dl.Spec.Target)
	reqs := make([]reconcile.Request, 0, len(names))
	for _, n := range names {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: dl.Namespace, Name: n}})
	}
	return reqs
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Episode ranked against it, two hops away: EpisodeSpec carries no
// QualityProfileRef, so this resolves profile -> Series (indexed) ->
// Episodes. Without it, an operator raising or lowering a profile's cutoff
// changed nothing observable on an Episode until some unrelated event woke
// it.
//
// QualityProfile is CLUSTER-scoped while Series is namespaced, so the Series
// List deliberately carries no client.InNamespace. The second hop filters a
// namespace-scoped Episode List in Go rather than using the
// ".spec.seriesRef" index: that index exists, but it is registered by the
// SERIES controller (series/reconciler.go's episodeBySeriesRefIndexKey), and
// a second IndexField call for the same (type, field) on one manager cache
// is a hard "indexer conflict" error at startup. Reaching across packages to
// share the key would couple this controller's setup to another's
// registration order for no gain on what is a cold path -- profiles are
// edited by hand, not by a controller.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var seriesList catalogv1alpha1.SeriesList
	if err := r.List(ctx, &seriesList, client.MatchingFields{seriesByQualityProfileIndexKey: qp.Name}); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, s := range seriesList.Items {
		var episodes catalogv1alpha1.EpisodeList
		if err := r.List(ctx, &episodes, client.InNamespace(s.Namespace)); err != nil {
			continue
		}
		for _, ep := range episodes.Items {
			if ep.Spec.SeriesRef != s.Name {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}})
		}
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "episode.Reconcile")
	defer span.End()
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
	// This reconciler has one status apply and no early return, so folding
	// the DLQ projector's annotation here puts it in every declaration.
	k8s.MarkDeadLettered(ep, &conditions)

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(ep.Namespace), client.MatchingFields{mediaFileByEpisodeIndexKey: ep.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	series, err := r.getSeries(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	profile, profileProblem, err := r.resolveProfile(ctx, ep, series)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"episode", ep.Name, "namespace", ep.Namespace,
			"seriesRef", ep.Spec.SeriesRef, "problem", profileProblem)
	}
	hasFile, fileRef, fileQuality, fileFormatScore, cutoffMet := FileState(mf, profile)
	if action, file := rollup.FileTransition(ep.Status.FileRef, mf); action != "" {
		r.publishFile(ctx, ep, action, file, mf, now)
	}

	dl, err := r.activeDownload(ctx, ep, series)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The overlay decides the phase only; whether the ref is set is
	// rollup.DownloadNonTerminal's call, made inside activeDownload.
	overlayPhase, _ := DownloadOverlay(dl)

	phase := Phase(monitored, ep.Status.AirDate, hasFile, cutoffMet, profile != nil, ep.Status.PendingGrab != nil, now)
	if overlayPhase != "" {
		phase = overlayPhase
	}

	aired := ep.Status.AirDate != nil && !now.Before(ep.Status.AirDate.Time)
	k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionAired, k8s.ReasonReconciled, "aired=%t", aired)
	if !aired {
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionAired, "Unaired", "air date has not passed yet")
	}
	// HasFile and CutoffMet are the other two conditions spec §4.2 lists for
	// Episode (alongside Aired). Until task C13 their only writer anywhere
	// was the MediaFile controller's rollupToOwner, which applied them under
	// this reconciler's own k8s.ManagerCatalogarr and therefore released
	// observedGeneration and activeDownloadRef on every apply. That rollup
	// is gone; the conditions move here, to the object's sole status writer.
	// See the movie package's identical block for the full rationale.
	if hasFile {
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionHasFile, "HasFile", "backed by MediaFile %s", ptr.Deref(fileRef, ""))
	} else {
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionHasFile, k8s.ReasonPending, "no MediaFile backs this episode")
	}
	switch {
	case !hasFile:
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, k8s.ReasonPending, "no file to rank against the profile cutoff")
	case profile == nil:
		if rollup.Transitioned(ep.Status.Conditions, catalogv1alpha1.EpisodeConditionCutoffMet, metav1.ConditionFalse, "ProfileUnresolved") {
			r.warn(ep, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
		}
		// The cutoff was NOT evaluated -- see the movie package's identical
		// branch for why this gets a reason of its own rather than reading
		// as a genuine CutoffUnmet.
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
	case cutoffMet:
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	default:
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
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
	if dl != nil {
		statusAC = statusAC.WithActiveDownloadRef(dl.Name)
	}
	// With no non-terminal Download, WithActiveDownloadRef is deliberately
	// not called -- see the identical rationale in the movie package's
	// reconciler.go. Since ruling R-5 this manager is the field's only
	// owner, so omitting it removes it.

	// This reconciler owns exactly Phase/Conditions/HasFile/FileRef/
	// FileQuality/FileFormatScore/CutoffMet/ActiveDownloadRef/
	// ObservedGeneration under k8s.ManagerCatalogarr. The Series reconciler
	// writes this Episode's provider-sourced fields
	// (Title/Overview/AirDate/TvdbID/RuntimeMinutes/AbsoluteNumber) under
	// the distinct k8s.ManagerCatalogarrSeries, so no pass-through of those
	// fields is needed here: server-side apply tracks
	// ownership per (manager name, field), and two different manager names
	// on the same object never collide or release each other's fields --
	// only two writers sharing ONE manager name do that (see this
	// package's doc comment and k8s.ManagerCatalogarrSeries's own comment
	// for the empirical finding that drove this split).
	//
	// status.finaleType and status.sceneNumbering are NOT in that list.
	// Nothing writes either one yet: ensureEpisode does not send finaleType
	// (metadata.Episode carries it, DesiredEpisode does not), and scene
	// numbering is M6 work. This comment used to claim finaleType among the
	// Series reconciler's fields, which would have made the next reader
	// believe a field was owned when it was merely declared.
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Episode(ep.Name, ep.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}
	// After the apply, so a phase that never landed is never announced; the
	// first phase an Episode gets is not an edge.
	if ep.Status.Phase != "" && ep.Status.Phase != phase {
		r.normal(ep, string(phase), "phase %s -> %s", ep.Status.Phase, phase)
	}

	if ep.Status.AirDate != nil && now.Before(ep.Status.AirDate.Time) {
		return ctrl.Result{RequeueAfter: ep.Status.AirDate.Sub(now)}, nil
	}
	return ctrl.Result{}, nil
}

// getSeries fetches the Series that owns ep (ep.Spec.SeriesRef), or nil when
// it is gone -- an Episode is only ever created by its Series, but that
// Series can be deleted (finalizer permitting) while the Episode is still
// being reconciled. Only a non-NotFound API error is returned.
func (r *Reconciler) getSeries(ctx context.Context, ep *catalogv1alpha1.Episode) (*catalogv1alpha1.Series, error) {
	var s catalogv1alpha1.Series
	if err := r.Get(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: ep.Spec.SeriesRef}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// resolveProfile resolves the QualityProfile episodes are ranked against via
// the owning Series (s, fetched once by getSeries; nil when it is gone ->
// Series.Spec.QualityProfileRef), since EpisodeSpec carries no
// QualityProfileRef of its own. It reports, as a non-empty problem string,
// any reason the profile could not be resolved: a missing Series, no
// reference on it, a reference that does not resolve, or a profile that
// exists but does not parse against the catalogue.
//
// Only a non-NotFound API error is fatal; every other outcome degrades to
// (nil, problem, nil) rather than failing the whole reconcile. Before task
// C13 all four outcomes collapsed into a bare nil profile and a
// cutoffMet=false indistinguishable from a genuine "this file is below the
// cutoff" -- see the CutoffMet condition's own branch.
//
// QualityProfile is cluster-scoped, so it is fetched by name alone.
func (r *Reconciler) resolveProfile(ctx context.Context, ep *catalogv1alpha1.Episode, s *catalogv1alpha1.Series) (*quality.Profile, string, error) {
	if s == nil {
		return nil, fmt.Sprintf("series %q not found", ep.Spec.SeriesRef), nil
	}
	if s.Spec.QualityProfileRef == "" {
		return nil, fmt.Sprintf("series %q has no spec.qualityProfileRef", s.Name), nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Name: s.Spec.QualityProfileRef}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("qualityProfile %q not found", s.Spec.QualityProfileRef), nil
		}
		return nil, "", err
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) > 0 {
		return nil, fmt.Sprintf("qualityProfile %q does not parse: %s", qp.Name, errors.Join(errs...)), nil
	}
	return &p, "", nil
}
