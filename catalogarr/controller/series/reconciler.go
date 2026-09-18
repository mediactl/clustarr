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

package series

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/version"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; series_types.go declares no SeriesConditionQueueFull constant
// of its own (unlike Series' Ready/MetadataReady/EpisodesSynced, which it
// does declare), so this is a local, package-scoped equivalent rather than
// an invented API constant.
const conditionQueueFull = "QueueFull"

// episodeBySeriesRefIndexKey indexes Episode by the Series that owns it
// (spec.seriesRef), so the reconciler can List a Series' own Episodes
// without scanning the whole namespace.
const episodeBySeriesRefIndexKey = ".spec.seriesRef"

// episodeSyncRPCBackoff is the RequeueAfter used when the episode-listing
// RPC fails, a concrete short backoff per §8.8 (RequeueAfter only, never a
// bare error-triggered exponential backoff for a known-transient
// dependency).
const episodeSyncRPCBackoff = 30 * time.Second

// EndedRecentWindow is how long after a series' most recent episode aired it
// is still treated as metadata.RefreshStateEndedRecent (a shorter refresh
// TTL) rather than metadata.RefreshStateEndedOld, once
// status.metadata.status reads "ended". This is a Clustarr-chosen default,
// not a value taken from Sonarr -- no verified source pins an exact cutoff
// here, mirroring movie.ReleasedRecentWindow's own disclaimer. Tune it in
// one place if it turns out wrong.
const EndedRecentWindow = 30 * 24 * time.Hour

// bus is the subset of events.Bus this reconciler actually calls: Publish
// for the metadata-staleness task and Request for the episode-listing RPC.
// Narrower than the full events.Requester (which also declares Serve, never
// called here) per the brief's Step 17 guidance ("less to fake and exactly
// what this reconciler uses") -- C12's registration line passes a real
// events.Bus value, which satisfies this interface structurally, so the
// narrowing is invisible to the wiring task.
type bus interface {
	events.Publisher
	Request(ctx context.Context, subject string, in, out any) error
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles a Series: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path and
// phase, and the episode-listing RPC fan-out into owned Episode objects. It
// is the sole writer of status.phase, status.path, status.seasons,
// status.episodeCount and status.episodeFileCount; status.metadata belongs
// to the metadata gateway (Task C5, field manager
// k8s.ManagerCatalogarrMetadata) and this reconciler never builds a
// SeriesStatusApplyConfiguration that calls WithMetadata.
//
// The per-Episode provider fields this reconciler writes
// (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes) are applied
// under the distinct k8s.ManagerCatalogarrSeries field manager, never
// k8s.ManagerCatalogarr (the Episode controller's own reconciler uses that
// one for Phase/Conditions/HasFile/etc). Two field manager NAMES, the same
// way grabarr/grabarr-engine split Download -- not the same name on
// disjoint fields by convention, which server-side apply does not actually
// keep disjoint (a same-manager apply that omits a field the manager
// previously sent releases it; see ManagerCatalogarrSeries's doc comment).
// All of these are EpisodeStatus fields, so this is a status-versus-status
// split within one subresource, not a spec-versus-status one like
// MediaFile's -- see this package's doc.go for the full reasoning.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Bus      bus

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Series controller: the finalizer/
// metadata-refresh predicate on Series itself, and Owns(&Episode{}) guarded
// by StatusFieldChanged on HasFile so an Episode's own HasFile flip (from
// the Episode controller's MediaFile watch) re-triggers this reconciler's
// Rollup, without this reconciler's own writes to the SAME owned Episodes
// (title/overview/airDate, never HasFile) looping it.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Episode{}, episodeBySeriesRefIndexKey,
		func(o client.Object) []string {
			ep, ok := o.(*catalogv1alpha1.Episode)
			if !ok {
				return nil
			}
			return []string{ep.Spec.SeriesRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("series").
		For(&catalogv1alpha1.Series{}, builder.WithPredicates(seriesPredicate())).
		Owns(&catalogv1alpha1.Episode{}, builder.WithPredicates(k8s.StatusFieldChanged(func(o client.Object) bool {
			ep, ok := o.(*catalogv1alpha1.Episode)
			return ok && ep.Status.HasFile
		}))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// seriesPredicate wakes this controller on a spec change (GenerationChanged)
// or on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) -- the same self-loop-avoidance shape as the
// Movie predicate.
func seriesPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			s, ok := o.(*catalogv1alpha1.Series)
			if !ok || s.Status.Metadata == nil {
				return metav1.Time{}
			}
			return s.Status.Metadata.RefreshedAt
		}),
	)
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var s catalogv1alpha1.Series
	if err := r.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&s) {
		return r.reconcileDelete(ctx, &s)
	}
	name, err := k8s.FinalizerFor(&s, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &s, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &s)
}

// reconcileDelete removes the finalizer. Owned Episodes are garbage
// collected by the apiserver via their controller reference; there is
// nothing else owned outside Kubernetes at this phase.
func (r *Reconciler) reconcileDelete(ctx context.Context, s *catalogv1alpha1.Series) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(s, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, s, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors the Movie reconciler's structure (addOptions
// recorded on every pass, metadata staleness decided and a MetadataTask
// published when needed, path computed once metadata is cached), then adds
// the episode-listing RPC fan-out as the last step, per §8.1's ordering: an
// RPC failure surfaces on EpisodesSynced/Phase but never blocks the rest of
// this same status patch from landing.
func (r *Reconciler) reconcileNormal(ctx context.Context, s *catalogv1alpha1.Series) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(s.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), s.Status.Conditions...)

	statusAC := catalogac.SeriesStatus().WithObservedGeneration(s.Generation)
	// Sent on every reconcile once the decision point is reached, not just
	// the first: see the identical rationale on movie.Reconciler's
	// reconcileNormal (SSA releases a field a manager stops sending).
	statusAC = statusAC.WithAddOptionsApplied(true)

	stale := s.Status.Metadata == nil
	if !stale {
		state := seriesRefreshState(s, now)
		ttl := metadata.RefreshTTL(commonv1.MediaKindSeries, state, s.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(s.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		// The envelope key is the <namespace>/<name> routing key every
		// worker parses to recover the namespace; the media key is the
		// subject token. They are not interchangeable -- the media key
		// is tokenised for the wire and has no slash to cut on.
		envKey := s.Namespace + "/" + s.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindSeries), s.Namespace, s.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: s.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(s.UID), s.Generation, "metadata"),
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
				k8s.MarkTrue(s, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, s)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Series(s.Name, s.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(s, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(s, &conditions, catalogv1alpha1.SeriesConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(s, &conditions, catalogv1alpha1.SeriesConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if s.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(s, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", s.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, s)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Series(s.Name, s.Namespace).WithStatus(statusAC)); perr != nil {
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
		nctx := naming.Context{
			Kind:        commonv1.MediaKindSeries,
			SeriesTitle: s.Status.Metadata.Title,
			SeriesYear:  int(s.Status.Metadata.Year),
			TvdbID:      strconv.FormatInt(s.Spec.TvdbID, 10),
		}
		pth, err := Path(rf.Spec.Path, s.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	// Episode-listing RPC fan-out (last step, per §8.1): a failure here
	// surfaces on EpisodesSynced/Phase without blocking the patch above.
	episodesSynced, episodes, syncErr := r.syncEpisodes(ctx, s, now)
	if syncErr != nil {
		k8s.MarkFalse(s, &conditions, catalogv1alpha1.SeriesConditionEpisodesSynced, "RPCError", "episode listing RPC failed: %s", syncErr.Error())
	} else {
		k8s.MarkTrue(s, &conditions, catalogv1alpha1.SeriesConditionEpisodesSynced, k8s.ReasonReconciled, "episodes synced")
	}

	seasons, episodeCount, episodeFileCount := Rollup(episodes)
	seasonACs := make([]*catalogac.SeasonStatusApplyConfiguration, 0, len(seasons))
	for _, ssn := range seasons {
		seasonACs = append(seasonACs, catalogac.SeasonStatus().
			WithNumber(ssn.Number).WithEpisodeCount(ssn.EpisodeCount).WithEpisodeFileCount(ssn.EpisodeFileCount))
	}
	statusAC = statusAC.WithSeasons(seasonACs...).WithEpisodeCount(episodeCount).WithEpisodeFileCount(episodeFileCount)

	phase := Phase(monitored, metaReady, episodesSynced)
	statusAC = statusAC.WithPhase(phase)

	k8s.MarkReady(s, &conditions, metaReady && episodesSynced && phase != catalogv1alpha1.SeriesPhasePending, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Series(s.Name, s.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: episodeSyncRPCBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/AddOptionsApplied/Conditions to statusAC, sourced from
// s's current (pre-reconcile) status. Used on the two early-return paths
// (QueueFull, RootFolderNotFound): both are transient failures -- a
// metadata publish hitting a full queue, or a RootFolder lookup that
// briefly 404s -- and without this, PatchStatus's apply would omit every
// field it does not mention, releasing (zeroing) a healthy Series'
// Path/Seasons/EpisodeCount/EpisodeFileCount/Phase the next time either
// blip happens. This is the same apply-release mechanism as movie's own
// reassertKnownStatus and the Series/Episode field-manager split, a third
// form of it in this wave: a healthy object sitting at Ready must not be
// reset to zero by a transient failure that never reaches the code
// recomputing those fields (the episode-listing fan-out and Rollup, below
// where this early return happens).
func reassertKnownStatus(statusAC *catalogac.SeriesStatusApplyConfiguration, s *catalogv1alpha1.Series) *catalogac.SeriesStatusApplyConfiguration {
	if s.Status.Phase != "" {
		statusAC = statusAC.WithPhase(s.Status.Phase)
	}
	if s.Status.Path != "" {
		statusAC = statusAC.WithPath(s.Status.Path)
	}
	seasonACs := make([]*catalogac.SeasonStatusApplyConfiguration, 0, len(s.Status.Seasons))
	for _, ssn := range s.Status.Seasons {
		seasonACs = append(seasonACs, catalogac.SeasonStatus().
			WithNumber(ssn.Number).WithEpisodeCount(ssn.EpisodeCount).WithEpisodeFileCount(ssn.EpisodeFileCount))
	}
	statusAC = statusAC.WithSeasons(seasonACs...)
	statusAC = statusAC.WithEpisodeCount(s.Status.EpisodeCount)
	statusAC = statusAC.WithEpisodeFileCount(s.Status.EpisodeFileCount)
	return statusAC
}

// syncEpisodes lists s's currently owned Episodes, requests its episode list
// from the metadata gateway, ensures each desired Episode exists with its
// provider-sourced status fields, and returns the full (pre-fan-out) owned
// Episode list for Rollup -- self-correcting: a newly created Episode's own
// Create event passes Owns()'s StatusFieldChanged predicate (Creates always
// pass), so a Rollup based on the pre-fan-out list here is completed by the
// very next reconcile it triggers, rather than needing a second List call
// against a cache that may not yet see this pass's own creates.
func (r *Reconciler) syncEpisodes(ctx context.Context, s *catalogv1alpha1.Series, now time.Time) (synced bool, existing []catalogv1alpha1.Episode, err error) {
	var episodeList catalogv1alpha1.EpisodeList
	if err := r.List(ctx, &episodeList, client.InNamespace(s.Namespace), client.MatchingFields{episodeBySeriesRefIndexKey: s.Name}); err != nil {
		return false, nil, err
	}
	existingNames := make(map[string]bool, len(episodeList.Items))
	for _, ep := range episodeList.Items {
		existingNames[ep.Name] = true
	}

	order := EffectiveEpisodeOrder(s.Spec.SeriesType, s.Spec.EpisodeOrder)
	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindEpisode,
		IDs: map[string]string{
			"tvdb":  strconv.FormatInt(s.Spec.TvdbID, 10),
			"order": string(order),
		},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, episodeSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return false, episodeList.Items, rpcErr
	}
	if resp.Error != "" {
		return false, episodeList.Items, fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]metadata.Episode, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var ep metadata.Episode
		if err := json.Unmarshal(raw, &ep); err != nil {
			return false, episodeList.Items, fmt.Errorf("decode episode: %w", err)
		}
		fetched = append(fetched, ep)
	}

	desired := DesiredEpisodes(s, s.Status.AddOptionsApplied, existingNames, fetched, now)
	for _, d := range desired {
		if err := r.ensureEpisode(ctx, s, d); err != nil {
			return false, episodeList.Items, err
		}
	}
	return true, episodeList.Items, nil
}

// ensureEpisode gets or creates the Episode named d.Name, then patches its
// provider-sourced status fields under k8s.ManagerCatalogarrSeries.
// spec.monitored is only ever set on Create (d.Monitored != nil there per
// DesiredEpisodes's contract); an already-existing Episode's spec.monitored
// is never touched, since it belongs to the user after creation.
//
// Field-clearing policy on a refresh that comes back with less data than a
// previous one: Title/Overview/RuntimeMinutes/TvdbID are always sent, so a
// provider that genuinely drops a value clears it here too; AirDate/
// AbsoluteNumber are only sent when non-nil, deliberately leaving (and so
// releasing, under SSA) a previously-cached value once the provider stops
// reporting it. See the inline comments below for why the split follows
// metadata.Episode's own types, and
// TestSeriesEnsureEpisodeProviderFieldRefresh for the pinning test.
func (r *Reconciler) ensureEpisode(ctx context.Context, s *catalogv1alpha1.Series, d DesiredEpisode) error {
	var ep catalogv1alpha1.Episode
	key := types.NamespacedName{Namespace: s.Namespace, Name: d.Name}
	err := r.Get(ctx, key, &ep)
	switch {
	case apierrors.IsNotFound(err):
		ep = catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: s.Namespace},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeriesRef:     s.Name,
				SeasonNumber:  d.SeasonNumber,
				EpisodeNumber: d.EpisodeNumber,
			},
		}
		if d.Monitored != nil {
			ep.Spec.Monitored = d.Monitored
		}
		if err := k8s.SetControllerReference(s, &ep, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &ep); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	case err != nil:
		return err
	}

	// Title, Overview, RuntimeMinutes and TvdbID are sent unconditionally,
	// even when the fresh value is the Go zero value ("" / 0): these are
	// plain scalars in metadata.Episode, so there is no way to tell "the
	// provider has no synopsis/runtime/tvdb id for this episode" apart from
	// "the provider's value happens to be empty/zero" -- both look the same
	// once they reach DesiredEpisode. Deciding to always send them means a
	// provider that genuinely drops a previously-known value is reflected
	// faithfully on the very next refresh, the same as Title already was
	// before this decision was made explicit (see
	// TestSeriesEnsureEpisodeProviderFieldRefresh, which pins exactly this).
	statusAC := catalogac.EpisodeStatus().
		WithTitle(d.Title).
		WithOverview(d.Overview).
		WithRuntimeMinutes(d.RuntimeMinutes).
		WithTvdbID(d.TvdbID)
	// AirDate and AbsoluteNumber keep their nil-guard: unlike the fields
	// above, metadata.Episode represents these as pointers, so the provider
	// DOES distinguish "no air date/absolute number for this episode" (nil)
	// from a real value. Omitting the field here when the pointer is nil is
	// deliberate: server-side apply releases a field this manager
	// previously sent and now omits (pkg/k8s.PatchStatus's doc), which is
	// how a value this manager cached on an earlier refresh gets cleared
	// once the provider stops sending it -- the same documented convention
	// as movie.Reconciler's ActiveDownloadRef clearing on !active.
	if d.AirDate != nil {
		statusAC = statusAC.WithAirDate(metav1.NewTime(*d.AirDate))
	}
	if d.AbsoluteNumber != nil {
		statusAC = statusAC.WithAbsoluteNumber(*d.AbsoluteNumber)
	}

	// k8s.ManagerCatalogarrSeries, not k8s.ManagerCatalogarr: this reconciler
	// writes an Episode it owns but does not itself compute the phase for,
	// and server-side apply replaces a manager's whole ownership set on
	// every apply -- two writers sharing one manager name on one object
	// would silently release each other's fields (confirmed empirically
	// against a real apiserver during this task's development; see
	// CLAUDE.md and k8s.ManagerCatalogarrSeries's own doc comment). A
	// distinct manager makes the split native: no re-assertion of the
	// Episode reconciler's own fields needed here, and if the two ever
	// genuinely claim the same field the apiserver reports a loud conflict
	// instead of losing data quietly.
	_, err = k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarrSeries, catalogac.Episode(d.Name, s.Namespace).WithStatus(statusAC))
	return err
}

// seriesRefreshState derives the metadata.RefreshTTL state bucket from a
// Series' own cached metadata and rollup status, per
// pkg/metadata/refresh.go's bucket names: continuing/upcoming map to
// RefreshStateContinuing (pkg/metadata/refresh.go has no "upcoming" bucket
// of its own -- flagged in the brief as a gap this task does not own);
// ended maps to RefreshStateEndedRecent when status.previousAiring is
// within EndedRecentWindow, else RefreshStateEndedOld.
//
// status.previousAiring, not a field on SeriesMetadata itself, is used as
// the "how long ago did it end" signal: SeriesMetadata carries no
// last-aired/ended-at field, and PreviousAiring's own doc comment ("when the
// most recent episode aired") is exactly what "recently ended" needs.
func seriesRefreshState(s *catalogv1alpha1.Series, now time.Time) string {
	switch s.Status.Metadata.Status {
	case catalogv1alpha1.SeriesRunStatusEnded:
		if s.Status.PreviousAiring != nil && now.Sub(s.Status.PreviousAiring.Time) < EndedRecentWindow {
			return metadata.RefreshStateEndedRecent
		}
		return metadata.RefreshStateEndedOld
	default: // continuing, upcoming
		return metadata.RefreshStateContinuing
	}
}
