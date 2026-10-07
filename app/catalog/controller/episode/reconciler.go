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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	seriesctl "github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// seriesByQualityProfileIndexKey indexes SERIES, not Episode, by the
	// QualityProfile it is ranked against: an Episode carries no
	// QualityProfileRef of its own, so the reverse hop from an edited
	// profile runs profile -> Series -> Episodes.
	seriesByQualityProfileIndexKey = ".spec.qualityProfileRef"
)

// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Episode: phase from monitored/airDate/hasFile/
// cutoffMet, and the file/download rollup from its MediaFiles (the
// remediation loop wakes it when one's rollup inputs move, loop spec §3.12)
// and its Downloads. It is the sole writer of status.phase, status.hasFile,
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

	// OnReconcile is a test-only hook, called at the top of every ReconcileItem.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// RegisterIndexes registers the QualityProfile index the watches' map
// functions read. An Episode's MediaFiles are found through the remediation
// loop's one item index (mfindex.Item, loop spec §3.16) and a Series'
// Episodes through seriesctl.EpisodeBySeriesRefIndex; its grabs are the
// Series' entries (ADR-0019 §6.1), so it registers no Download index.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.Series{}, seriesByQualityProfileIndexKey,
		func(o client.Object) []string {
			s, ok := o.(*catalogv1alpha1.Series)
			if !ok || s.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{s.Spec.QualityProfileRef}
		})
}

// Watches is the Episode's item path on the remediation loop (loop spec
// §3.12; S3, S4, S9, S10): every watch that wakes an Episode except its
// files'. A Series' Episodes are found through
// seriesctl.EpisodeBySeriesRefIndex. The AudioGraft watch stays until F7.3
// (S23).
func (r *Reconciler) Watches() []rollup.Watch {
	return []rollup.Watch{
		{Object: &catalogv1alpha1.Episode{}, Map: rollup.Self, Predicates: []predicate.Predicate{episodePredicate()}},
		{
			Object: &transcodev1alpha1.AudioGraft{}, Map: rollup.ItemOfAudioGraft(commonv1.MediaKindEpisode),
			Predicates: []predicate.Predicate{k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(rollup.AudioGraftState))},
		},
		{Object: &catalogv1alpha1.QualityProfile{}, Map: r.mapQualityProfile, Predicates: []predicate.Predicate{k8s.GenerationChanged()}},
		{Object: &catalogv1alpha1.Series{}, Map: r.mapSeries, Predicates: []predicate.Predicate{seriesMonitoredChanged()}},
	}
}

var _ rollup.Item = (*Reconciler)(nil)

// episodePredicate wakes an Episode on a spec change (GenerationChanged,
// e.g. a user editing spec.monitored) or on the Series reconciler's own
// write of status.airDate (StatusFieldChanged) -- a newly-discovered air
// date must wake it immediately, not wait for the next RequeueAfter poll.
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

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Episode ranked against it, two hops away: EpisodeSpec carries no
// QualityProfileRef, so this resolves profile -> Series (indexed) ->
// Episodes. Without it, an operator raising or lowering a profile's cutoff
// changed nothing observable on an Episode until some unrelated event woke
// it.
//
// QualityProfile is CLUSTER-scoped while Series is namespaced, so the Series
// List deliberately carries no client.InNamespace. The second hop reads
// seriesctl.EpisodeBySeriesRefIndex: a namespace List filtered in Go ran once per
// Series for every QualityProfile the cache's initial sync delivered, the
// O(watched x listed) map function CLAUDE.md warns about.
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
	for i := range seriesList.Items {
		reqs = append(reqs, r.mapSeries(ctx, &seriesList.Items[i])...)
	}
	return reqs
}

// mapSeries is every Episode of a Series, through seriesctl.EpisodeBySeriesRefIndex:
// turning the Series' monitoring on or off changes each one's phase.
func (r *Reconciler) mapSeries(ctx context.Context, o client.Object) []reconcile.Request {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok {
		return nil
	}
	var episodes catalogv1alpha1.EpisodeList
	if err := r.List(ctx, &episodes, client.InNamespace(s.Namespace), client.MatchingFields{seriesctl.EpisodeBySeriesRefIndex: s.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(episodes.Items))
	for _, ep := range episodes.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}})
	}
	return reqs
}

// seriesMonitoredChanged passes a Series update that turns its monitoring
// on or off, and nothing else: not the initial sync's creates (the Episode
// source enqueues every Episode already), and no other edit, which changes
// no Episode's phase.
func seriesMonitoredChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, okOld := e.ObjectOld.(*catalogv1alpha1.Series)
			n, okNew := e.ObjectNew.(*catalogv1alpha1.Series)
			if !okOld || !okNew {
				return false
			}
			// Monitoring, the profile (anime classification patches it) and
			// the original language (the metadata gateway fills it later)
			// all decide an episode's cutoff, WrongLanguage and audio state.
			origOld, origNew := "", ""
			if o.Status.Metadata != nil {
				origOld = o.Status.Metadata.OriginalLanguage
			}
			if n.Status.Metadata != nil {
				origNew = n.Status.Metadata.OriginalLanguage
			}
			return ptr.Deref(o.Spec.Monitored, true) != ptr.Deref(n.Spec.Monitored, true) ||
				o.Spec.QualityProfileRef != n.Spec.QualityProfileRef || origOld != origNew
		},
	}
}

// ReconcileItem is the Episode's item path on the remediation loop (loop
// spec §3.12); it keeps the §8.8 skeleton: get, split on deletion, ensure
// the finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "episode.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var ep catalogv1alpha1.Episode
	if err := r.Get(ctx, nn, &ep); err != nil {
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

// reconcileNormal computes hasFile/cutoffMet from its MediaFiles
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
	if err := r.List(ctx, &mfList, client.InNamespace(ep.Namespace), client.MatchingFields{mfindex.Item: mfindex.ItemKey(commonv1.MediaKindEpisode, ep.Name)}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	series, err := r.getSeries(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Sonarr searches an episode only when its series is monitored too.
	if series != nil && !ptr.Deref(series.Spec.Monitored, true) {
		monitored = false
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
	// A transcoded file is final: see the movie package's identical line.
	transcoded := rollup.Transcoded(mf)
	// A file whose probed audio lacks the profile's language (anime
	// dual-audio spec §5.3) reads CutoffUnmet, so the wanted sweep looks for
	// a replacement -- unless it is transcoded, which stays final.
	originalTag := ""
	if series != nil && series.Status.Metadata != nil {
		originalTag = series.Status.Metadata.OriginalLanguage
	}
	audio := rollup.ProbedAudioLanguages(mf)
	wrongLanguage := profile != nil && hasFile && decision.LacksLanguage(*profile, originalTag, audio)
	if wrongLanguage && !transcoded {
		cutoffMet = false
	}
	if action, file := rollup.FileTransition(ep.Status.FileRef, mf); action != "" {
		r.publishFile(ctx, ep, action, file, mf, now)
	}

	dl, donorOpen := downloadView(ctx, ep, series)
	ag, err := r.audioGraft(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The overlay decides the phase only; whether the ref is set is
	// rollup.DownloadNonTerminal's call, made inside activeDownload.
	overlayPhase, _ := DownloadOverlay(dl)

	phase := Phase(monitored, ep.Status.AirDate, hasFile, transcoded, cutoffMet, profile != nil, ep.Status.PendingGrab != nil, now)
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
	case transcoded:
		// Ahead of the profile arms, as in the movie package: a transcoded
		// file is final and meets the cutoff whatever the profile says.
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, rollup.ReasonTranscoded, "the file is transcoded, and a transcoded file is final")
	case profile == nil:
		if rollup.Transitioned(ep.Status.Conditions, catalogv1alpha1.EpisodeConditionCutoffMet, metav1.ConditionFalse, "ProfileUnresolved") {
			r.warn(ep, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
		}
		// The cutoff was NOT evaluated -- see the movie package's identical
		// branch for why this gets a reason of its own rather than reading
		// as a genuine CutoffUnmet.
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
	case wrongLanguage:
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "WrongLanguage", "the file's audio lacks the profile's language")
	case cutoffMet:
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	default:
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
	}

	if wrongLanguage {
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionWrongLanguage, "WrongLanguage", "the file's audio %v lacks the profile's language", audio)
	} else {
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionWrongLanguage, "LanguageOK", "no evidence the file's audio is wrong")
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
	if a := rollup.AudioStateFor(profile, originalTag, mf, rollup.GraftObservation{DonorDownloading: donorOpen, Graft: ag}); a != nil {
		statusAC = statusAC.WithAudio(rollup.AudioStateAC(a))
	}
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if dl != nil {
		// The covering entry's id on the Series, and its phase as the
		// Episode's downloadPhase (ADR-0019 §6.2).
		statusAC = statusAC.WithActiveDownloadRef(dl.ID).WithDownloadPhase(dl.Phase)
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
	if conflicted, err := itemstatus.Apply(ctx, r.Client, ep, catalogac.Episode(ep.Name, ep.Namespace).WithStatus(statusAC)); err != nil || conflicted {
		return itemstatus.Requeue(conflicted), err
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
