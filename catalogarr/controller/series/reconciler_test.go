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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
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
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus combinedBus) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	r := &series.Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("series"), //nolint:staticcheck // matches C12's run.go registration line verbatim
		Bus:      bus,
	}
	require.NoError(t, r.SetupWithManager(mgr))

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

	c := startManager(t, ctx, cfg, bus)
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
			return cond != nil && cond.Status == metav1.ConditionFalse
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
}

func mustGet(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Series {
	t.Helper()
	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	return got
}

func ptrBoolTrue(p *bool) bool { return p != nil && *p }

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
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10),
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

// fakePublisher is a tiny local Publisher that always returns err, used to
// prove the QueueFull path without spinning up a DiscardNew membus stream.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}
