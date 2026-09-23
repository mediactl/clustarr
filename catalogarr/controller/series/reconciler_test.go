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

package series_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/series"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func init() {
	ctrl.SetLogger(logging.LogrBridge(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
}

func newTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, env.Stop())
	})
	return cfg
}

func newBareTestClient(t *testing.T) client.Client {
	t.Helper()
	cfg := newTestConfig(t)
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
}

// fakeEpisodeRPC implements the episode-listing half of the reconciler's
// bus.Request, per the brief's ASSUMED CONTRACT: Kind: MediaKindEpisode,
// IDs: {tvdb, order}, Results [][]byte of JSON metadata.Episode.
type fakeEpisodeRPC struct {
	episodes []metadata.Episode
	err      error
}

func (f fakeEpisodeRPC) Request(_ context.Context, _ string, _, out any) error {
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		return errors.New("unexpected out type")
	}
	resp.Kind = commonv1.MediaKindEpisode
	for _, ep := range f.episodes {
		b, err := json.Marshal(ep)
		if err != nil {
			return err
		}
		resp.Results = append(resp.Results, b)
	}
	return nil
}

// combinedBus satisfies the reconciler's narrowed bus interface
// (events.Publisher + events.Requester) by pairing a real events.Publisher
// (for the metadata-staleness path) with a swappable Request implementation
// (for the episode-listing RPC), so a single Reconciler can be reused
// across subtests with different fake RPC behaviours.
type combinedBus struct {
	events.Publisher
	requester interface {
		Request(ctx context.Context, subject string, in, out any) error
	}
}

func (c combinedBus) Request(ctx context.Context, subject string, in, out any) error {
	return c.requester.Request(ctx, subject, in, out)
}

// startManager wires a real series.Reconciler into a real ctrl.Manager
// backed by the envtest apiserver, starts it, and waits for the cache to
// sync. controller-runtime enforces controller-name uniqueness with a
// process-global registry (see the movie package's identical note), so
// every scenario needing the real, auto-wired controller lives as a t.Run
// under one Test function that calls this exactly once.
// reconcileCounter counts Reconcile invocations across every object the
// manager's controller processes, so a test can assert on the delta around
// a specific operation without a data race.
type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc()         { c.n.Add(1) }
func (c *reconcileCounter) count() int64 { return c.n.Load() }

func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus combinedBus) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	counter := &reconcileCounter{}
	r := &series.Reconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("series"), // matches run.go's registration line verbatim
		Bus:         bus,
		OnReconcile: counter.inc,
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// startCacheOnly starts a real ctrl.Manager's cache (so syncEpisodes'
// field-indexed List works) WITHOUT wiring a series.Reconciler's watches to
// it, so nothing auto-reconciles and SetupWithManager (and its
// process-global "series" controller name) is never touched. Used by tests
// that call r.Reconcile() directly instead of going through a running
// controller. The one field index registered here must stay in sync with
// series.Reconciler.SetupWithManager's own registration.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Episode{}, ".spec.seriesRef",
		func(o client.Object) []string {
			ep, ok := o.(*catalogv1alpha1.Episode)
			if !ok {
				return nil
			}
			return []string{ep.Spec.SeriesRef}
		}))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

func waitForPhase(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Series {
	t.Helper()
	var got catalogv1alpha1.Series
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

// TestSeriesReconcilerRealController is the one Test function that starts a
// real, auto-wired series.Reconciler (see startManager's doc for why there
// can only be one per test binary).
func TestSeriesReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	realBus := membus.New(nil)
	require.NoError(t, realBus.Ensure(ctx, events.Default()))

	// The default Request implementation (no episodes, no error) is swapped
	// per subtest via requester's mutable field.
	requester := &fakeEpisodeRPC{}
	bus := combinedBus{Publisher: realBus, requester: requester}

	c, counter := startManager(t, ctx, cfg, bus)
	require.NoError(t, c.Create(ctx, testNamespace("series-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("series-ns", "tv-root", "/data/media/tv")))

	t.Run("finalizer add does not early-return, addOptions applied, metadata publish", func(t *testing.T) {
		requester.episodes, requester.err = nil, nil

		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: "series-ns"},
			Spec: catalogv1alpha1.SeriesSpec{
				TvdbID: 79126, QualityProfileRef: "none", RootFolderRef: "tv-root",
			},
		}
		require.NoError(t, c.Create(ctx, s))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Series{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/series", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "the-wire"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			return hasFinalizer && got.Status.Phase == catalogv1alpha1.SeriesPhasePending && got.Status.AddOptionsApplied
		}, 5*time.Second, 20*time.Millisecond,
			"finalizer, addOptionsApplied and status.phase=Pending must all appear from the same reconcile pass")

		cond := k8s.FindCondition(mustGet(t, ctx, c, "series-ns", "the-wire").Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
	})

	t.Run("episode fan-out: anime absolute numbering, idempotent re-fanout, user edit survives", func(t *testing.T) {
		abs1, abs2 := int32(1091), int32(1092)
		requester.err = nil
		requester.episodes = []metadata.Episode{
			{SeasonNumber: 1, EpisodeNumber: 1091, AbsoluteNumber: &abs1, Title: "Episode 1091"},
			{SeasonNumber: 1, EpisodeNumber: 1092, AbsoluteNumber: &abs2, Title: "Episode 1092"},
		}

		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "one-piece", Namespace: "series-ns"},
			Spec: catalogv1alpha1.SeriesSpec{
				TvdbID: 81797, QualityProfileRef: "none", RootFolderRef: "tv-root",
				SeriesType: catalogv1alpha1.SeriesTypeAnime,
				AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
			},
		}
		require.NoError(t, c.Create(ctx, s))

		metaAC := catalogac.Series(s.Name, s.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(
				catalogac.SeriesMetadata().WithTitle("One Piece").WithYear(1999).
					WithStatus(catalogv1alpha1.SeriesRunStatusContinuing).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)

		var eps catalogv1alpha1.EpisodeList
		require.Eventually(t, func() bool {
			if err := c.List(ctx, &eps, client.InNamespace("series-ns"), client.MatchingLabels{}); err != nil {
				return false
			}
			n := 0
			for _, ep := range eps.Items {
				if ep.Spec.SeriesRef == "one-piece" {
					n++
				}
			}
			return n == 2
		}, 5*time.Second, 20*time.Millisecond, "expected 2 Episode objects fanned out from one-piece")

		byName := map[string]catalogv1alpha1.Episode{}
		for _, ep := range eps.Items {
			if ep.Spec.SeriesRef == "one-piece" {
				byName[ep.Name] = ep
			}
		}
		ep1091, ok := byName["one-piece-s01e1091"]
		require.True(t, ok)
		ep1092, ok := byName["one-piece-s01e1092"]
		require.True(t, ok)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece-s01e1091"}, &got); err != nil {
				return false
			}
			return got.Status.AbsoluteNumber != nil && *got.Status.AbsoluteNumber == 1091 && got.Status.Title == "Episode 1091"
		}, 5*time.Second, 20*time.Millisecond)

		ref := k8s.ControllerRef(&ep1091)
		require.NotNil(t, ref)
		assert.Equal(t, "one-piece", ref.Name)
		assert.True(t, ptrBoolTrue(ep1091.Spec.Monitored))
		assert.True(t, ptrBoolTrue(ep1092.Spec.Monitored))

		require.NotNil(t, waitForPhase(t, ctx, c, "series-ns", "one-piece"))
		cond := k8s.FindCondition(mustGet(t, ctx, c, "series-ns", "one-piece").Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)

		// The user edits one episode's spec.monitored out of band. A merge
		// patch, not a Get-mutate-Update, avoids a spurious 409 against the
		// live controller's own concurrent status writes -- see the
		// identical rationale in the movie package's reconciler_test.go.
		patch := client.MergeFrom(ep1091.DeepCopy())
		falseVal := false
		ep1091.Spec.Monitored = &falseVal
		require.NoError(t, c.Patch(ctx, &ep1091, patch))

		// Force a second reconcile of the Series (a spec bump; a status-only
		// patch would not pass seriesPredicate).
		var s2 catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece"}, &s2))
		specPatch := client.MergeFrom(s2.DeepCopy())
		s2.Spec.Tags = []string{"bumped"}
		require.NoError(t, c.Patch(ctx, &s2, specPatch))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece"}, &got); err != nil {
				return false
			}
			return got.Generation == s2.Generation && got.Status.ObservedGeneration == got.Generation
		}, 5*time.Second, 20*time.Millisecond)

		var eps2 catalogv1alpha1.EpisodeList
		require.NoError(t, c.List(ctx, &eps2, client.InNamespace("series-ns")))
		n := 0
		for _, ep := range eps2.Items {
			if ep.Spec.SeriesRef == "one-piece" {
				n++
			}
		}
		assert.Equal(t, 2, n, "re-fanout with the same provider data must not duplicate Episodes")

		var gotEp1091 catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece-s01e1091"}, &gotEp1091))
		require.NotNil(t, gotEp1091.Spec.Monitored)
		assert.False(t, *gotEp1091.Spec.Monitored, "the reconciler must not clobber a user's spec.monitored edit on an existing episode")
	})

	t.Run("episode fan-out RPC error does not block Phase/Path/MetadataReady", func(t *testing.T) {
		requester.episodes = nil
		requester.err = errors.New("timeout")

		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "rpc-error-series", Namespace: "series-ns"},
			Spec: catalogv1alpha1.SeriesSpec{
				TvdbID: 12345, QualityProfileRef: "none", RootFolderRef: "tv-root",
			},
		}
		require.NoError(t, c.Create(ctx, s))

		metaAC := catalogac.Series(s.Name, s.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(
				catalogac.SeriesMetadata().WithTitle("RPC Error Series").WithYear(2020).
					WithStatus(catalogv1alpha1.SeriesRunStatusContinuing).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)

		var got catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "rpc-error-series"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced)
			if cond == nil || cond.Status != metav1.ConditionFalse {
				return false
			}
			// Wait for the object to CONVERGE, not merely for one condition.
			// The pass that flips EpisodesSynced need not be the pass that set
			// MetadataReady and Path: a finalizer-add conflict ("the object has
			// been modified") makes an earlier pass return before its status
			// apply, so a snapshot satisfying one condition can be missing the
			// others entirely. Asserting the rest off that snapshot failed
			// roughly half of cold runs.
			//
			// This waits on PRESENCE and asserts VALUES below, deliberately: if
			// it waited on the values, a genuinely wrong one would surface as a
			// bare 5s timeout instead of a message naming the field.
			return k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady) != nil &&
				got.Status.Path != ""
		}, 5*time.Second, 20*time.Millisecond)

		metaReadyCond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady)
		require.NotNil(t, metaReadyCond)
		assert.Equal(t, metav1.ConditionTrue, metaReadyCond.Status, "MetadataReady must still be set correctly despite the episode RPC failure")
		assert.Equal(t, "/data/media/tv/RPC Error Series (2020) [tvdbid-12345]", got.Status.Path,
			"Path must still be computed despite the episode RPC failure")
	})

	// Series' Owns(&Episode{}) watch: an owned Episode's own HasFile flip
	// (simulating the Episode controller's own MediaFile watch, landed
	// later in this same task) re-triggers Rollup without a direct
	// MediaFile/Download watch on Series itself. Reuses the one-piece
	// fixture from the fan-out subtest above, per the brief's note that a
	// single combined envtest is fine here.
	t.Run("Owns(Episode) watch keeps Rollup live on an owned Episode's HasFile flip", func(t *testing.T) {
		epAC := catalogac.Episode("one-piece-s01e1091", "series-ns").WithStatus(
			catalogac.EpisodeStatus().WithHasFile(true),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, epAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece"}, &got); err != nil {
				return false
			}
			return got.Status.EpisodeFileCount == 1
		}, 5*time.Second, 20*time.Millisecond)

		var got catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "one-piece"}, &got))
		require.Len(t, got.Status.Seasons, 1)
		assert.EqualValues(t, 1, got.Status.Seasons[0].EpisodeFileCount)
		assert.EqualValues(t, 2, got.Status.Seasons[0].EpisodeCount)
	})

	// The concrete proof that seriesPredicate (GenerationChanged Or
	// StatusFieldChanged on status.metadata.refreshedAt) wakes this
	// controller when the metadata gateway writes status.metadata, but this
	// controller's own status write (which never touches status.metadata)
	// does not loop it -- the same shape as the movie package's
	// TestMovieReconcilerMetadataRefreshSelfLoopGuard subtest. It runs last
	// and in its own namespace so the reconcileCounter delta, taken from a
	// snapshot immediately before its own writes, is not confused by any
	// other subtest's residual work.
	t.Run("metadata refresh triggers reconcile but the controller's own write does not loop", func(t *testing.T) {
		requester.episodes, requester.err = nil, nil

		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "selfloop-series", Namespace: "series-ns"},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 99999, QualityProfileRef: "none", RootFolderRef: "tv-root"},
		}
		require.NoError(t, c.Create(ctx, s))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "selfloop-series"}, &got); err != nil {
				return false
			}
			return got.Status.Phase != ""
		}, 5*time.Second, 20*time.Millisecond, "the initial create must reconcile to a settled phase")

		n := counter.count()

		metaAC := catalogac.Series(s.Name, s.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(
				catalogac.SeriesMetadata().WithTitle("Self Loop Series").WithYear(2020).
					WithStatus(catalogv1alpha1.SeriesRunStatusContinuing).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool { return counter.count() > n }, 5*time.Second, 20*time.Millisecond,
			"the gateway's metadata write must wake this controller")

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-ns", Name: "selfloop-series"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 20*time.Millisecond)

		n2 := counter.count()
		assert.Equal(t, int64(1), n2-n, "the gateway write must cause exactly one reconcile, not a cascade")

		require.Never(t, func() bool {
			return counter.count() > n2
		}, 500*time.Millisecond, 20*time.Millisecond,
			"this controller's own status patch must not re-trigger itself")
	})
}

func mustGet(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Series {
	t.Helper()
	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	return got
}

func ptrBoolTrue(p *bool) bool { return p != nil && *p }

// TestSeriesEpisodeFieldManagersStayDisjoint is the mandatory two-writer
// gate the coordinator's ruling calls for, modeled on
// catalogarr/controller/mediafile's TestMediaFileFieldManagersStayDisjoint:
// Series's real ensureEpisode applies the provider fields under
// k8s.ManagerCatalogarrSeries, a simulated Episode-reconciler write applies
// its own computed fields under k8s.ManagerCatalogarr, and managedFields is
// decoded directly (not inferred from the object's final values alone) to
// prove each manager owns exactly its own field set and neither write lost
// the other's data -- including after a second Series reconcile, proving
// the split needs no ongoing re-assertion from either side.
//
// Unlike MediaFile's spec-versus-status split, both sides here write to the
// SAME subresource: Title/Overview/AirDate/TvdbID/RuntimeMinutes/
// AbsoluteNumber/FinaleType are EpisodeStatus fields, not EpisodeSpec
// (confirmed against episode_types.go -- EpisodeSpec holds only
// SeriesRef/SeasonNumber/EpisodeNumber, immutable and set once at Create,
// never through server-side apply, plus Monitored, which Series also only
// ever sets once at Create). So this is a status-versus-status split
// exactly like grabarr/grabarr-engine's on DownloadStatus (§5): distinct
// field manager NAMES on disjoint fields within one subresource, not a
// subresource split. That is what makes it correct, and it is why the
// assertions below check specific field-path names via statusFieldNames
// rather than just "an entry for each manager exists" -- two managers both
// present on the same subresource proves nothing about disjointness on
// their own.
func TestSeriesEpisodeFieldManagersStayDisjoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("fieldmanager-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("fieldmanager-ns", "tv-root", "/data/media/tv")))

	requester := &fakeEpisodeRPC{episodes: []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
	}}
	bus := combinedBus{Publisher: fakePublisher{}, requester: requester}
	r := &series.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}

	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "field-manager-series", Namespace: "fieldmanager-ns"},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 55555, QualityProfileRef: "none", RootFolderRef: "tv-root",
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, s))

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "fieldmanager-ns", Name: "field-manager-series"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	epKey := types.NamespacedName{Namespace: "fieldmanager-ns", Name: "field-manager-series-s01e01"}
	var ep catalogv1alpha1.Episode
	// The cache is eventually consistent: c.Get right after r.Reconcile
	// (which wrote via k8s.PatchStatus, straight to the API server) can
	// observe a resourceVersion older than what the server already has,
	// until the informer's watch stream catches up. Poll rather than a
	// single Get, the same pattern used throughout the movie/episode
	// packages' own reconciler tests.
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, epKey, &ep); err != nil {
			return false
		}
		return ep.Status.Title == "Pilot"
	}, 5*time.Second, 10*time.Millisecond, "Series' real ensureEpisode never landed status.title")

	// Series' write landed under catalogarr-series, claiming exactly the
	// provider field it set (title) and nothing Episode-owned.
	seriesFields := managedStatusFieldPaths(ep.ManagedFields, "catalogarr-series")
	require.NotNil(t, seriesFields, "no catalogarr-series/status entry in managedFields: %+v", fieldManagerNames(ep.ManagedFields))
	seriesStatusNames := statusFieldNames(seriesFields)
	assert.True(t, seriesStatusNames["title"], "catalogarr-series should own status.title")
	assert.False(t, seriesStatusNames["phase"], "catalogarr-series must not claim status.phase")
	assert.False(t, seriesStatusNames["hasFile"], "catalogarr-series must not claim status.hasFile")

	// Simulate the Episode reconciler's own write: its computed fields,
	// under the distinct k8s.ManagerCatalogarr.
	epAC := catalogac.Episode(ep.Name, ep.Namespace).WithStatus(
		catalogac.EpisodeStatus().
			WithPhase(catalogv1alpha1.EpisodePhaseImported).
			WithHasFile(true).
			WithFileFormatScore(10).
			WithCutoffMet(true),
	)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, epAC)
	require.NoError(t, err)

	// Same eventually-consistent-cache reasoning as above: this Get must
	// not run before the informer has observed the write it just made.
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, epKey, &ep); err != nil {
			return false
		}
		return ep.Status.Phase == catalogv1alpha1.EpisodePhaseImported
	}, 5*time.Second, 10*time.Millisecond, "the simulated Episode write never landed status.phase")
	// Neither write lost the other's data.
	assert.Equal(t, "Pilot", ep.Status.Title, "the Episode reconciler's simulated write must not clobber Series' provider field")
	assert.Equal(t, catalogv1alpha1.EpisodePhaseImported, ep.Status.Phase)
	assert.True(t, ep.Status.HasFile)

	for _, want := range []string{"catalogarr-series", "catalogarr"} {
		if !managesField(ep.ManagedFields, want, "status") {
			t.Errorf("no %q/status entry in managedFields: %+v", want, fieldManagerNames(ep.ManagedFields))
		}
	}
	seriesFields = managedStatusFieldPaths(ep.ManagedFields, "catalogarr-series")
	require.NotNil(t, seriesFields)
	seriesStatusNames = statusFieldNames(seriesFields)
	assert.True(t, seriesStatusNames["title"])
	assert.False(t, seriesStatusNames["phase"], "catalogarr-series must still not claim status.phase after the Episode write")
	assert.False(t, seriesStatusNames["hasFile"], "catalogarr-series must still not claim status.hasFile after the Episode write")

	episodeFields := managedStatusFieldPaths(ep.ManagedFields, "catalogarr")
	require.NotNil(t, episodeFields, "no catalogarr/status entry in managedFields: %+v", fieldManagerNames(ep.ManagedFields))
	episodeStatusNames := statusFieldNames(episodeFields)
	assert.True(t, episodeStatusNames["phase"], "catalogarr should own status.phase")
	assert.True(t, episodeStatusNames["hasFile"], "catalogarr should own status.hasFile")
	assert.False(t, episodeStatusNames["title"], "catalogarr must not claim status.title")

	// A second Series reconcile (re-fanout with the same provider data)
	// must not erase the Episode-owned fields the simulated write just
	// set -- proving the split needs no ongoing re-assertion from either
	// side, unlike the same-manager arrangement this replaces.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, epKey, &ep))
	assert.Equal(t, "Pilot", ep.Status.Title)
	assert.Equal(t, catalogv1alpha1.EpisodePhaseImported, ep.Status.Phase, "Series' second apply must not release Episode's phase")
	assert.True(t, ep.Status.HasFile, "Series' second apply must not release Episode's hasFile")
}

// managedStatusFieldPaths decodes the (manager, "status") entry's FieldsV1
// -- server-side apply's per-field-path ownership record -- into its
// top-level field-path set. Modeled on the mediafile package's
// managedFieldPaths helper.
func managedStatusFieldPaths(entries []metav1.ManagedFieldsEntry, manager string) map[string]any {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == "status" && e.FieldsV1 != nil {
			var m map[string]any
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// statusFieldNames returns the bare (un-prefixed, non-".") field names a
// managedStatusFieldPaths result's "f:status" sub-map claims.
func statusFieldNames(fields map[string]any) map[string]bool {
	out := map[string]bool{}
	status, ok := fields["f:status"].(map[string]any)
	if !ok {
		return out
	}
	for k := range status {
		if k == "." {
			continue
		}
		out[strings.TrimPrefix(k, "f:")] = true
	}
	return out
}

func managesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource {
			return true
		}
	}
	return false
}

func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}

// TestSeriesReconcilerQueueFull proves ErrQueueFull from Publish sets
// QueueFull=True and requeues after exactly one minute (§Step 6, mirrored
// from Movie). It uses a bare, uncached client with no controller wired to
// it at all: the QueueFull branch returns before reconcileNormal ever
// reaches a field-indexed List, and running without a second manager keeps
// this the only reconciler ever touching the object.
func TestSeriesReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("series-queuefull-ns")))

	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse", Namespace: "series-queuefull-ns"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 280619, QualityProfileRef: "none", RootFolderRef: "none"},
	}
	require.NoError(t, c.Create(ctx, s))

	r := &series.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeEpisodeRPC{}},
	}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "series-queuefull-ns", Name: "the-expanse"}})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "series-queuefull-ns", Name: "the-expanse"}, &got))
	cond := k8s.FindCondition(got.Status.Conditions, "QueueFull")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

// TestSeriesReconcilerTransientFailuresPreserveSteadyState is the review's
// mandatory regression case, the Series analogue of
// movie.TestMovieReconcilerTransientFailuresPreserveSteadyState:
// TestSeriesReconcilerQueueFull runs against a freshly-created object with
// no prior status, so there is nothing for a buggy early return to
// release, and RootFolderNotFound has no coverage at all. This drives a
// Series to a genuine steady state (fresh metadata, a resolved Path, a
// real episode fan-out with one episode carrying a file, and
// Phase=Ready) FIRST, then triggers each transient failure and asserts
// Path/Seasons/EpisodeCount/EpisodeFileCount/Phase all survive. Without
// the reassertKnownStatus fix, both early returns build a status apply
// containing only ObservedGeneration/AddOptionsApplied/Conditions and
// PatchStatus releases everything else this manager previously sent -- a
// healthy Series reset to zero by a blip in a metadata-queue publish or a
// momentary RootFolder lookup failure, with its conditions left
// describing the phase it no longer has.
func TestSeriesReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("series-transient-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("series-transient-ns", "tv-root", "/data/media/tv")))

	driveToReady := func(t *testing.T, name string, tvdbID int64) reconcile.Request {
		t.Helper()
		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "series-transient-ns"},
			Spec: catalogv1alpha1.SeriesSpec{
				TvdbID: tvdbID, QualityProfileRef: "none", RootFolderRef: "tv-root",
				AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
			},
		}
		require.NoError(t, c.Create(ctx, s))

		metaAC := catalogac.Series(s.Name, s.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(
				catalogac.SeriesMetadata().WithTitle("Steady State").WithYear(2020).
					WithStatus(catalogv1alpha1.SeriesRunStatusContinuing).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, types.NamespacedName{Namespace: "series-transient-ns", Name: name}, &got); err != nil {
				return false
			}
			return got.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "series-transient-ns", Name: name}}
		requester := &fakeEpisodeRPC{episodes: []metadata.Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"},
		}}
		bus := combinedBus{Publisher: fakePublisher{}, requester: requester}
		r := &series.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		epKey := types.NamespacedName{Namespace: "series-transient-ns", Name: name + "-s01e01"}
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			return c.Get(ctx, epKey, &got) == nil
		}, 5*time.Second, 10*time.Millisecond, "setup: episode fan-out never created s01e01")

		// Give one episode a file directly (as if the Episode controller's
		// own MediaFile watch had set it), so EpisodeFileCount rolls up to a
		// non-zero value -- a released-to-zero bug would otherwise be
		// indistinguishable from an already-zero count.
		epAC := catalogac.Episode(name+"-s01e01", "series-transient-ns").WithStatus(
			catalogac.EpisodeStatus().WithHasFile(true),
		)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, epAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, epKey, &got); err != nil {
				return false
			}
			return got.Status.HasFile
		}, 5*time.Second, 10*time.Millisecond)

		// A second reconcile picks up the episode's HasFile in this pass's
		// Rollup.
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		// Phase was already Ready after the FIRST reconcile (episodesSynced
		// does not depend on episodeCount), so polling on Phase alone here
		// could observe a cache read still stale from that first pass, one
		// whose EpisodeCount/EpisodeFileCount predate this second
		// reconcile's write landing. Poll on EpisodeFileCount instead -- the
		// field this second pass's Rollup is the only thing that can ever
		// set to 1 -- so the eventual read is guaranteed to be at least as
		// new as this reconcile's own patch.
		var got catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.EpisodeFileCount == 1
		}, 5*time.Second, 10*time.Millisecond, "setup: series never rolled up the episode's file")
		require.Equal(t, catalogv1alpha1.SeriesPhaseReady, got.Status.Phase)
		require.NotEmpty(t, got.Status.Path)
		require.Equal(t, int32(2), got.Status.EpisodeCount)
		require.Len(t, got.Status.Seasons, 1)
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "series-queuefull-steady", 9001)

		var before catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		// Force a fresh staleness decision on the next reconcile by
		// backdating status.metadata.refreshedAt well past the RefreshTTL,
		// exactly as a real metadata-gateway write would eventually
		// require a new fetch.
		oldRefresh := metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))
		staleAC := catalogac.Series(before.Name, before.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(
				catalogac.SeriesMetadata().WithTitle("Steady State").WithYear(2020).
					WithStatus(catalogv1alpha1.SeriesRunStatusContinuing).
					WithRefreshedAt(oldRefresh),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, staleAC)
		require.NoError(t, err)
		// A tolerant "is it old now" check, not exact equality: the
		// apiserver round-trips metav1.Time through RFC 3339 at
		// one-second precision, so the in-memory oldRefresh (full Go
		// time.Time precision) never exactly equals what a subsequent Get
		// reads back.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &series.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeEpisodeRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// Same cache-staleness reasoning as elsewhere: poll for the
		// QueueFull condition before reading the rest of the object,
		// rather than a single Get immediately after Reconcile's own
		// PatchStatus.
		var after catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, catalogv1alpha1.SeriesPhaseReady, after.Status.Phase, "QueueFull must not release Phase")
		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.Equal(t, before.Status.EpisodeCount, after.Status.EpisodeCount, "QueueFull must not release EpisodeCount")
		assert.Equal(t, before.Status.EpisodeFileCount, after.Status.EpisodeFileCount, "QueueFull must not release EpisodeFileCount")
		assert.Equal(t, before.Status.Seasons, after.Status.Seasons, "QueueFull must not release Seasons")
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "series-rootfolder-steady", 9002)

		var before catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		// A plain Update, not a merge patch: this main-resource write
		// never touches the status subresource, and startCacheOnly has no
		// auto-wired controller racing this write, so there is no
		// concurrent-writer reason to prefer a patch here -- see the
		// identical rationale in the movie package's reconciler_test.go.
		var withMissingRoot catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, req.NamespacedName, &withMissingRoot))
		withMissingRoot.Spec.RootFolderRef = "does-not-exist"
		require.NoError(t, c.Update(ctx, &withMissingRoot))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Series
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &series.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeEpisodeRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// Same cache-staleness reasoning as elsewhere: poll for the
		// RootFolderNotFound reason before reading the rest of the
		// object.
		var after catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Reason == "RootFolderNotFound"
		}, 5*time.Second, 10*time.Millisecond)
		cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "RootFolderNotFound", cond.Reason)

		assert.Equal(t, catalogv1alpha1.SeriesPhaseReady, after.Status.Phase, "RootFolderNotFound must not release Phase")
		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.Equal(t, before.Status.EpisodeCount, after.Status.EpisodeCount, "RootFolderNotFound must not release EpisodeCount")
		assert.Equal(t, before.Status.EpisodeFileCount, after.Status.EpisodeFileCount, "RootFolderNotFound must not release EpisodeFileCount")
		assert.Equal(t, before.Status.Seasons, after.Status.Seasons, "RootFolderNotFound must not release Seasons")
	})
}

// TestSeriesEnsureEpisodeProviderFieldRefresh is the review's Important
// finding: ensureEpisode's provider-sourced fields must have an explicit,
// tested policy for what happens when a later refresh comes back with a
// value this manager previously sent now blank/nil, not an accidental one.
// The decision this test pins: Title, Overview and RuntimeMinutes (plain
// scalars, where the provider has no way to distinguish "no data" from a
// real empty/zero) are sent unconditionally on every ensureEpisode call, so
// a provider that genuinely drops a synopsis or runtime is reflected
// faithfully, exactly like Title already was. AirDate and AbsoluteNumber
// (pointers, where the provider DOES distinguish "no data" via nil) keep
// their guard -- omitting the field when the pointer is nil deliberately
// releases (clears) a previously-cached value under SSA, the same
// documented convention as movie.Reconciler's ActiveDownloadRef clearing.
// TvdbID is asserted here too: it was missing from the fan-out entirely
// until this same round (self-discovered against the brief's Step 17 field
// list, not a review finding), so this is also its only coverage through
// the real ensureEpisode path.
func TestSeriesEnsureEpisodeProviderFieldRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("refresh-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("refresh-ns", "tv-root", "/data/media/tv")))

	airDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	absNum := int32(7)
	requester := &fakeEpisodeRPC{episodes: []metadata.Episode{
		{
			SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", Overview: "A synopsis.",
			AirDate: &airDate, Runtime: 42, AbsoluteNumber: &absNum,
			IDs: metadata.ExternalIDs{metadata.KeyTVDB: "6053919"},
		},
	}}
	bus := combinedBus{Publisher: fakePublisher{}, requester: requester}
	r := &series.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}

	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "refresh-series", Namespace: "refresh-ns"},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 66666, QualityProfileRef: "none", RootFolderRef: "tv-root",
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, s))

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "refresh-ns", Name: "refresh-series"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	epKey := types.NamespacedName{Namespace: "refresh-ns", Name: "refresh-series-s01e01"}
	var full catalogv1alpha1.Episode
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, epKey, &full); err != nil {
			return false
		}
		return full.Status.RuntimeMinutes == 42
	}, 5*time.Second, 10*time.Millisecond, "setup: first fan-out never landed the full provider record")

	assert.Equal(t, "Pilot", full.Status.Title)
	assert.Equal(t, "A synopsis.", full.Status.Overview)
	require.NotNil(t, full.Status.AirDate)
	assert.True(t, full.Status.AirDate.Time.Equal(airDate))
	require.NotNil(t, full.Status.AbsoluteNumber)
	assert.EqualValues(t, 7, *full.Status.AbsoluteNumber)
	assert.EqualValues(t, 6053919, full.Status.TvdbID)

	// A later refresh: same episode (season/episode match, so ensureEpisode
	// updates the existing Episode rather than creating a new one), but the
	// provider now sends a blank overview/runtime and no air date/absolute
	// number at all -- exactly the shape a provider drop looks like.
	requester.episodes = []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
	}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// RuntimeMinutes 42 -> 0 is an unambiguous state transition (unlike
	// polling on AirDate == nil, which could spuriously match a read still
	// stale from before the first reconcile's write), so it is what this
	// poll waits on before reading the rest of the object.
	var cleared catalogv1alpha1.Episode
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, epKey, &cleared); err != nil {
			return false
		}
		return cleared.Status.RuntimeMinutes == 0
	}, 5*time.Second, 10*time.Millisecond, "the blank refresh never landed")

	assert.Equal(t, "Pilot", cleared.Status.Title, "Title is unaffected by this refresh")
	assert.Empty(t, cleared.Status.Overview, "Overview must be sent unconditionally, clearing the stale synopsis")
	assert.Zero(t, cleared.Status.RuntimeMinutes, "RuntimeMinutes must be sent unconditionally, clearing the stale runtime")
	assert.Nil(t, cleared.Status.AirDate, "AirDate must be released (cleared) when the provider sends no air date")
	assert.Nil(t, cleared.Status.AbsoluteNumber, "AbsoluteNumber must be released (cleared) when the provider sends no absolute number")
}

// fakePublisher is a tiny local Publisher that always returns err, used to
// prove the QueueFull path without spinning up a DiscardNew membus stream.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}
