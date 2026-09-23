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

package movie_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
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
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
	"github.com/mediactl/clustarr/catalogarr/controller/wantedcron"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func init() {
	ctrl.SetLogger(logging.LogrBridge(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
}

// newTestConfig starts a real envtest apiserver with the Clustarr CRDs
// installed and returns the rest.Config, per pkg/k8s/patch_envtest_test.go's
// newTestClient pattern -- adjusted to hand back the config too, since this
// suite also needs to start a real ctrl.Manager (a client alone cannot
// register field indices or serve watches).
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

// reconcileCounter counts Reconcile invocations across every object the
// manager's controller processes, so a test can assert on the delta around a
// specific operation without a data race.
type reconcileCounter struct {
	n atomic.Int64
}

func (c *reconcileCounter) inc() { c.n.Add(1) }

func (c *reconcileCounter) count() int64 { return c.n.Load() }

// startManager wires a real movie.Reconciler into a real ctrl.Manager backed
// by the envtest apiserver, starts it, and waits for the cache to sync. It
// returns the manager's own cached client (required for the MediaFile and
// Download field-indexed List calls the reconciler makes -- those indices
// are registered on the manager's cache in SetupWithManager and are not
// served by a bare client.New against the live apiserver) and a counter
// wired to OnReconcile.
//
// controller-runtime enforces controller-name uniqueness with a
// process-global registry (pkg/controller/name.go's checkName), not a
// per-manager one, so calling this -- and therefore SetupWithManager, which
// names the controller "movie" -- more than once in this test binary panics
// with "controller with name movie already exists". Every scenario that
// needs the real, auto-wired controller therefore lives as a t.Run under one
// Test function, TestMovieReconcilerRealController, which calls this
// exactly once.
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus events.Publisher) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	counter := &reconcileCounter{}
	r := &movie.Reconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("movie"), // matches run.go's registration line verbatim
		Bus:         bus,
		OnReconcile: counter.inc,
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// startCacheOnly starts a real ctrl.Manager's cache (so the MediaFile and
// Download field indices work) WITHOUT wiring a movie.Reconciler's watches
// to it, so nothing auto-reconciles and SetupWithManager (and its
// process-global "movie" controller name) is never touched. Tests that call
// r.Reconcile() directly for tight, synchronous control over the return
// value (RequeueAfter, a specific error) use this instead of startManager:
// mixing a manually invoked Reconcile with an auto-wired controller racing
// the SAME newly-created (finalizer-less) object is a genuine hazard -- both
// would call k8s.EnsureFinalizer's plain, optimistic-concurrency Update
// concurrently, and the loser gets a 409 conflict. The field indices come
// from movie.RegisterIndexes, the same call SetupWithManager makes.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, movie.RegisterIndexes(ctx, mgr.GetFieldIndexer()))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

// waitForCachedMetadata polls the cached client until it observes
// status.metadata set, so a subsequent manual r.Reconcile call reads an
// object whose cache entry is at least as new as the write this test just
// made. Skipping this and reconciling immediately after a write is a known
// hazard: the cache is eventually consistent, so a Get can return a
// resourceVersion older than what the server already has, and this
// reconciler's finalizer-add (a plain, optimistic-concurrency Update) then
// fails with a 409 that has nothing to do with the behaviour under test.
func waitForCachedMetadata(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Movie
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// waitForPhase polls the cached client until it observes a non-empty
// status.phase and returns the settled object. A manual r.Reconcile call, or
// the auto-wired controller's own async pass, writes through PatchStatus
// (server-side apply, direct to the API server); the cache-backed client
// used for follow-up assertions or further writes only reflects that write
// once its informer's watch stream catches up, so reading or writing
// immediately afterward risks the same stale-read hazard
// waitForCachedMetadata guards against, and (when a further plain Update
// follows, such as EnsureFinalizer on the next reconcile of a still
// in-flight object) can produce a spurious 409.
func waitForPhase(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Movie {
	t.Helper()
	var got catalogv1alpha1.Movie
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMovie},
	}
}

func testQualityProfile(ns, name string, tierQualities ...string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Cutoff:    "cutoff",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "cutoff", Qualities: tierQualities},
			},
		},
	}
}

func downloadStatusAC(name, ns string, phase downloadv1alpha1.DownloadPhase) *downloadac.DownloadApplyConfiguration {
	return downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase))
}

// fakePublisher is a tiny local Publisher that always returns err, used to
// prove the QueueFull path without spinning up a DiscardNew membus stream.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}

// TestMovieReconcilerRealController is the one Test function that starts a
// real, auto-wired movie.Reconciler against a real envtest apiserver (see
// startManager's doc for why there can only be one). Every subtest uses its
// own namespace and require.Eventually rather than a manual r.Reconcile
// call, so there is exactly one writer -- the auto-wired controller itself
// -- touching each object.
func TestMovieReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	c, counter := startManager(t, ctx, cfg, bus)
	require.NoError(t, c.Create(ctx, testNamespace("finalizer-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("metadata-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("avail-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("selfloop-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("delayed-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("avail-ns", "movies-root", "/data/media/movies")))
	require.NoError(t, c.Create(ctx, testRootFolder("selfloop-ns", "movies-root", "/data/media/movies")))
	require.NoError(t, c.Create(ctx, testRootFolder("delayed-ns", "movies-root", "/data/media/movies")))

	// The grab worker's status.pendingGrab write must both WAKE this
	// controller and be folded into Phase. Neither was true before: Phase
	// took no pendingGrab input and reached Delayed only through an
	// already-created Download, and moviePredicate fired on generation and
	// status.metadata.refreshedAt only -- so a worker writing pendingGrab did
	// not even schedule a reconcile. Without both halves the entire
	// delay-profile feature is invisible: the movie sits at Wanted for the
	// whole delay window.
	t.Run("a worker's pendingGrab write wakes this controller and reaches Delayed", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "inception", Namespace: "delayed-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 27205, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		// Drive it to a settled, metadata-ready steady state first: Phase
		// must be Wanted before the pendingGrab write, or the assertion
		// below could not tell Delayed apart from "never reconciled".
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Movie(m.Name, m.Namespace).WithStatus(
				catalogac.MovieStatus().WithMetadata(
					catalogac.MovieMetadata().WithTitle("Inception").WithYear(2010).
						WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()),
				),
			))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "delayed-ns", Name: "inception"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseWanted
		}, 10*time.Second, 20*time.Millisecond, "the movie must settle at Wanted before the delay is applied")

		// Exactly what catalogarr/worker/grab writes: pendingGrab, never
		// Phase, under the grab path's own field manager. It carries no
		// status.metadata, and does not have to: k8s.ManagerCatalogarrGrab
		// and k8s.ManagerCatalogarrMetadata own disjoint field sets, so
		// neither apply releases the other's.
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
			catalogac.Movie(m.Name, m.Namespace).WithStatus(
				catalogac.MovieStatus().WithPendingGrab(
					catalogac.PendingGrab().
						WithReleaseTitle("Inception.2010.1080p.BluRay.x264-GROUP").
						WithProtocol(commonv1.ProtocolTorrent).
						WithGrabAt(metav1.NewTime(time.Now().Add(45*time.Minute))),
				),
			))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "delayed-ns", Name: "inception"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseDelayed
		}, 10*time.Second, 20*time.Millisecond,
			"the pendingGrab write must wake this controller and recompute Phase=Delayed")
	})

	// finalizer add does not early-return (§14): status.phase is already
	// Pending in the very first reconcile pass that adds the finalizer, not
	// only on a later watch event.
	t.Run("finalizer add does not early-return", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "inception", Namespace: "finalizer-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 27205, QualityProfileRef: "missing-profile", RootFolderRef: "missing-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Movie{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/movie", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "finalizer-ns", Name: "inception"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			return hasFinalizer && got.Status.Phase == catalogv1alpha1.MoviePhasePending
		}, 5*time.Second, 20*time.Millisecond,
			"finalizer and status.phase=Pending must both appear from the same reconcile pass")
	})

	// addOptionsApplied is set exactly once and stays true across repeated
	// reconciles (§Step 5).
	t.Run("addOptions applied exactly once", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "metadata-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 949, QualityProfileRef: "none", RootFolderRef: "none",
				AddOptions: catalogv1alpha1.MovieAddOptions{Monitor: catalogv1alpha1.MovieMonitorMovieAndCollection},
			},
		}
		require.NoError(t, c.Create(ctx, m))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "heat"}, &got); err != nil {
				return false
			}
			return got.Status.AddOptionsApplied
		}, 5*time.Second, 20*time.Millisecond)

		// A merge patch, not a Get-mutate-Update: see the identical comment
		// below in the MediaFile-watch subtest for why a plain Update here
		// risks a spurious 409 against the controller's own concurrent
		// status writes.
		var before catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "heat"}, &before))
		patch := client.MergeFrom(before.DeepCopy())
		before.Spec.Tags = []string{"bumped"}
		require.NoError(t, c.Patch(ctx, &before, patch))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "heat"}, &got); err != nil {
				return false
			}
			return got.Generation == before.Generation && got.Status.ObservedGeneration == got.Generation
		}, 5*time.Second, 20*time.Millisecond)

		var got1, got2 catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "heat"}, &got1))
		time.Sleep(100 * time.Millisecond)
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "heat"}, &got2))
		assert.True(t, got1.Status.AddOptionsApplied)
		assert.True(t, got2.Status.AddOptionsApplied)
		assert.Equal(t, got1.Status.Phase, got2.Status.Phase, "a settled object's phase must not keep flipping")
	})

	// A Movie with no cached metadata publishes a MetadataTask and reports
	// MetadataReady=False/Pending (§Step 6 happy path).
	t.Run("metadata missing publishes a MetadataTask and reports Pending", func(t *testing.T) {
		mediaKey := events.MediaKey(string(commonv1.MediaKindMovie), "metadata-ns", "the-matrix")
		wantSubject := events.WorkMetadataSubject(events.PriorityNormal, mediaKey)

		received := make(chan *events.Envelope, 4)
		stop, err := bus.Subscribe(ctx, events.Subscription{
			Stream:  events.StreamWorkCatalogarr,
			Durable: "test-watcher-the-matrix",
			Filters: []string{wantSubject},
		}, func(_ context.Context, m events.Message) error {
			received <- m.Envelope()
			return nil
		})
		require.NoError(t, err)
		defer stop()

		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: "metadata-ns"},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 603, QualityProfileRef: "none", RootFolderRef: "none"},
		}
		require.NoError(t, c.Create(ctx, m))

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "metadata-ns", Name: "the-matrix"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhasePending
		}, 5*time.Second, 20*time.Millisecond)

		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionMetadataReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)

		wantID := events.MsgIDForObject(string(got.UID), got.Generation, "metadata")

		var envelope *events.Envelope
		select {
		case envelope = <-received:
		case <-time.After(5 * time.Second):
			t.Fatalf("no MetadataTask published on %s within 5s", wantSubject)
		}

		assert.Equal(t, wantID, envelope.ID)
		var task schema.MetadataTask
		require.NoError(t, schema.Decode(envelope.Schema, envelope.Data, &task))
		assert.Equal(t, commonv1.MediaKindMovie, task.MediaRef.Kind)
		assert.Equal(t, "the-matrix", task.MediaRef.Name)

		// The envelope key and the subject's media key are different things
		// and must not be conflated. Every consumer recovers the namespace
		// with strings.Cut(env.Key, "/") -- catalogarr/metadata/worker.go,
		// the grab handler, the rss matcher and the search worker all do it,
		// and all of them events.Discard to the DLQ when the cut fails. A
		// media key is tokenised for the wire and has no slash left to cut
		// on, so publishing one as the envelope key dead-letters every task
		// on first delivery.
		ns, name, ok := strings.Cut(envelope.Key, "/")
		require.True(t, ok, "envelope key %q must be <namespace>/<name>", envelope.Key)
		assert.Equal(t, "metadata-ns", ns)
		assert.Equal(t, "the-matrix", name)
		assert.NotContains(t, mediaKey, "/", "a media key is a single subject token")
	})

	// A watched MediaFile rolls up HasFile/FileRef/FileQuality/CutoffMet and
	// drives Phase=Imported or CutoffUnmet (§Step 22).
	t.Run("MediaFile watch rolls up HasFile and reaches Imported or CutoffUnmet", func(t *testing.T) {
		// Modifier must be the real ModifierNone sentinel ("none"), not the Go
		// zero value (""): pkg/quality's canonical Bluray-1080p Definition sets
		// it explicitly, and Profile.Index compares the full (Source,
		// Resolution, Modifier) triple for a video quality.
		bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}

		require.NoError(t, c.Create(ctx, testQualityProfile("avail-ns", "cutoff-met-at-1080p", "Bluray-1080p")))
		require.NoError(t, c.Create(ctx, testQualityProfile("avail-ns", "cutoff-is-4k-remux", "Remux-2160p")))

		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "heat-3", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 949, QualityProfileRef: "cutoff-met-at-1080p", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Heat").WithYear(1995).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		// Settle the create+metadata reconcile (finalizer added, base phase
		// computed, and -- since the QualityProfile fetch is unconditional --
		// its informer warmed) before introducing the MediaFile, so the
		// MediaFile-triggered reconcile is not racing the controller's own
		// still-in-flight first pass over a plain, optimistic-concurrency
		// Update (the finalizer add).
		waitForPhase(t, ctx, c, "avail-ns", "heat-3")

		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "heat-3-abc1234567", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat-3"},
				Path:     "/data/media/movies/Heat (1995)/Heat.mkv",
				Quality:  bluray,
			},
		}
		require.NoError(t, c.Create(ctx, mf))

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3"}, &got); err != nil {
				return false
			}
			return got.Status.HasFile
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.FileRef)
		assert.Equal(t, "heat-3-abc1234567", *got.Status.FileRef)
		assert.Equal(t, catalogv1alpha1.MoviePhaseImported, got.Status.Phase)

		// Spec §4.2's two file-derived conditions. Task C13 moved them here
		// from the MediaFile controller's rollup (which was releasing this
		// manager's other fields on every apply); asserting them on the
		// WATCH path is what proves deleting that rollup lost nothing.
		hasFileCond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionHasFile)
		require.NotNil(t, hasFileCond, "the MediaFile watch must raise the HasFile condition")
		assert.Equal(t, metav1.ConditionTrue, hasFileCond.Status)
		cutoffCond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet)
		require.NotNil(t, cutoffCond, "the MediaFile watch must raise the CutoffMet condition")
		assert.Equal(t, metav1.ConditionTrue, cutoffCond.Status)

		// A stricter profile (cutoff at 4k remux) reaches CutoffUnmet instead.
		// A merge patch, not a Get-mutate-Update, avoids a spurious 409: the
		// controller may still be actively patching this object's status
		// (SSA, its own separate field set) around the same moment, and a
		// plain Update's optimistic-concurrency precondition would reject a
		// spec-only change built from a resourceVersion the controller's own
		// write has since moved past.
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3"}, &got))
		patch := client.MergeFrom(got.DeepCopy())
		got.Spec.QualityProfileRef = "cutoff-is-4k-remux"
		require.NoError(t, c.Patch(ctx, &got, patch))

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseCutoffUnmet
		}, 5*time.Second, 20*time.Millisecond)

		cutoffCond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet)
		require.NotNil(t, cutoffCond)
		assert.Equal(t, metav1.ConditionFalse, cutoffCond.Status, "a file below the cutoff must lower the CutoffMet condition")
		assert.Equal(t, "CutoffUnmet", cutoffCond.Reason)
		hasFileCond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionHasFile)
		require.NotNil(t, hasFileCond)
		assert.Equal(t, metav1.ConditionTrue, hasFileCond.Status, "the file is still there, only the profile changed")

		// Editing the PROFILE itself -- the realistic "operator lowers the
		// bar" action -- must reach this Movie on its own, with nothing
		// else touched and no MediaFile nudged. Only the QualityProfile
		// watch can deliver that; without it a cutoff edit changed nothing
		// observable until some unrelated event happened to wake the item.
		// Tiers are ordered best-first and CutoffMet is idx <= cutoffIndex,
		// so putting Bluray-1080p in the cutoff tier brings the existing
		// file back up to it.
		var qp catalogv1alpha1.QualityProfile
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "cutoff-is-4k-remux"}, &qp))
		qpPatch := client.MergeFrom(qp.DeepCopy())
		qp.Spec.Tiers = []catalogv1alpha1.Tier{{Name: "cutoff", Qualities: []string{"Bluray-1080p", "Remux-2160p"}}}
		qp.Spec.Cutoff = "cutoff"
		require.NoError(t, c.Patch(ctx, &qp, qpPatch))

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseImported
		}, 5*time.Second, 20*time.Millisecond, "a QualityProfile edit alone must re-rank the movie")
		cutoffCond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet)
		require.NotNil(t, cutoffCond)
		assert.Equal(t, metav1.ConditionTrue, cutoffCond.Status)

		// Deleting the MediaFile must clear the rollup. This is the case
		// the deleted mediafile rollup structurally could not handle -- it
		// only ever ran while a file existed and hard-coded HasFile=true --
		// and it is the argument that made option 1 strictly better rather
		// than merely equivalent, so it is pinned here.
		var doomed catalogv1alpha1.MediaFile
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3-abc1234567"}, &doomed))
		require.NoError(t, c.Delete(ctx, &doomed))

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "heat-3"}, &got); err != nil {
				return false
			}
			return !got.Status.HasFile
		}, 5*time.Second, 20*time.Millisecond, "deleting the MediaFile must clear HasFile")
		assert.Nil(t, got.Status.FileRef, "a deleted MediaFile must release FileRef")
		assert.False(t, got.Status.CutoffMet)
		assert.NotEqual(t, catalogv1alpha1.MoviePhaseImported, got.Status.Phase)
		assert.NotEqual(t, catalogv1alpha1.MoviePhaseCutoffUnmet, got.Status.Phase)

		// The negative branches of both relocated conditions, which run on
		// every file-less reconcile and were previously unasserted.
		hasFileCond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionHasFile)
		require.NotNil(t, hasFileCond)
		assert.Equal(t, metav1.ConditionFalse, hasFileCond.Status)
		assert.Equal(t, k8s.ReasonPending, hasFileCond.Reason)
		cutoffCond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet)
		require.NotNil(t, cutoffCond)
		assert.Equal(t, metav1.ConditionFalse, cutoffCond.Status)
		assert.Equal(t, k8s.ReasonPending, cutoffCond.Reason, "with no file the cutoff was not evaluated either")
	})

	// A profile that cannot be resolved must not read as a genuine
	// "your file is below the cutoff": that is the answer that parks the
	// item in the search rotation looking like a legitimate upgrade
	// candidate. The condition carries the reason, and the phase reads
	// CutoffUnevaluated -- never CutoffUnmet, which the wanted sweep chases.
	t.Run("an unresolvable QualityProfile reports ProfileUnresolved, not CutoffUnmet", func(t *testing.T) {
		bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}

		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "tenet", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 577922, QualityProfileRef: "no-such-profile", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Tenet").WithYear(2020).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		waitForPhase(t, ctx, c, "avail-ns", "tenet")

		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "tenet-abc1234567", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "tenet"},
				Path:     "/data/media/movies/Tenet (2020)/Tenet.mkv",
				Quality:  bluray,
			},
		}
		require.NoError(t, c.Create(ctx, mf))

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "tenet"}, &got); err != nil {
				return false
			}
			return got.Status.HasFile
		}, 5*time.Second, 20*time.Millisecond)

		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "ProfileUnresolved", cond.Reason,
			"a dangling qualityProfileRef must not masquerade as a file below the cutoff")
		assert.Contains(t, cond.Message, "no-such-profile")
		assert.Equal(t, catalogv1alpha1.MoviePhaseCutoffUnevaluated, got.Status.Phase,
			"the phase column must not read CutoffUnmet for a file never ranked against a cutoff")
	})

	// Ruling R-5: this reconciler is the only writer of
	// status.activeDownloadRef, deriving it from the Movie's own non-terminal
	// Download. The Download is created the way the grab path creates one --
	// a server-side apply under k8s.ManagerCatalogarrGrab carrying an
	// ownerReference to the Movie -- and NOTHING writes the ref: the grab
	// worker's old write is gone, so the ref can only come from the Download
	// watch and the derivation. managedFields then proves the handover, not
	// just the value: a stray co-owner would leave every value assertion
	// passing (CLAUDE.md, "A double-claim is silent").
	t.Run("an owned Download sets and clears activeDownloadRef with no grab-worker write, and only catalogarr owns it", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 329865, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		live := waitForPhase(t, ctx, c, "avail-ns", "arrival")
		require.Nil(t, live.Status.ActiveDownloadRef, "setup: no Download exists yet")

		dl := grabPathDownload(t, ctx, c, &live, "arrival-abc1234567")

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef != nil
		}, 5*time.Second, 20*time.Millisecond, "a new owned Download must reach the ref through the Download watch alone")
		assert.Equal(t, dl, *got.Status.ActiveDownloadRef)
		assert.Equal(t, []string{k8s.ManagerCatalogarr.String()}, statusFieldOwners(t, &got, "activeDownloadRef"),
			"status.activeDownloadRef must have exactly one owner, the Movie reconciler")

		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC(dl, "avail-ns", downloadv1alpha1.DownloadPhaseAssigned))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseDownloading
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.ActiveDownloadRef)
		assert.Equal(t, dl, *got.Status.ActiveDownloadRef)

		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC(dl, "avail-ns", downloadv1alpha1.DownloadPhaseImported))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef == nil
		}, 5*time.Second, 20*time.Millisecond, "an Imported Download must clear the ref")
		assert.Empty(t, statusFieldOwners(t, &got, "activeDownloadRef"), "a cleared ref leaves no owner behind")

		// The phase edges were reported as Events through the real recorder.
		require.Eventually(t, func() bool {
			return hasEvent(ctx, c, "avail-ns", "arrival", string(catalogv1alpha1.MoviePhaseDownloading))
		}, 5*time.Second, 50*time.Millisecond, "the Downloading edge must be reported as an Event on the Movie")
	})

	// Gap-fix R-12: the phase for EVERY DownloadPhase value, on a Movie in
	// its steady state (Wanted: metadata fresh, available, no file), and the
	// wanted sweep's own selection (wantedcron.ListCandidates, which both
	// halves of the sweep read) agreeing with it. Each phase is driven from
	// the opposite side -- a terminal phase first for a non-terminal one,
	// and the reverse -- so every step is an observable edge, not a state
	// the previous step already satisfied.
	t.Run("every DownloadPhase gives the phase the wanted sweep agrees with", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "prisoners", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 146233, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Movie(m.Name, m.Namespace).WithStatus(catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Prisoners").WithYear(2013).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()))))
		require.NoError(t, err)

		// settled waits for the Movie to read want (and to hold the ref or
		// not), then asserts the sweep's reason for it.
		settled := func(t *testing.T, step string, want catalogv1alpha1.MoviePhase, dl string) {
			t.Helper()
			var got catalogv1alpha1.Movie
			require.Eventually(t, func() bool {
				if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "prisoners"}, &got); err != nil {
					return false
				}
				hasRef := got.Status.ActiveDownloadRef != nil && *got.Status.ActiveDownloadRef == dl
				return got.Status.Phase == want && hasRef == (want == catalogv1alpha1.MoviePhaseDownloading)
			}, 5*time.Second, 20*time.Millisecond, "%s: want phase %s", step, want)

			cands, err := wantedcron.ListCandidates(ctx, c, func(k commonv1.MediaKind) bool { return k == commonv1.MediaKindMovie },
				time.Now(), client.InNamespace("avail-ns"))
			require.NoError(t, err)
			wantReason := schema.SearchReason("")
			if want == catalogv1alpha1.MoviePhaseWanted {
				wantReason = schema.SearchReasonMissing
			}
			for _, cand := range cands {
				if cand.Ref.Name == "prisoners" {
					assert.Equal(t, wantReason, cand.Reason, "%s: the wanted sweep must agree with phase %s", step, want)
					return
				}
			}
			t.Fatalf("%s: the sweep did not list the movie", step)
		}
		settled(t, "steady state", catalogv1alpha1.MoviePhaseWanted, "")

		live := waitForPhase(t, ctx, c, "avail-ns", "prisoners")
		dl := grabPathDownload(t, ctx, c, &live, "prisoners-abc1234567")
		settled(t, `a Download with no phase yet`, catalogv1alpha1.MoviePhaseDownloading, dl)

		set := func(p downloadv1alpha1.DownloadPhase) {
			_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC(dl, "avail-ns", p))
			require.NoError(t, err)
		}
		for _, p := range []downloadv1alpha1.DownloadPhase{
			downloadv1alpha1.DownloadPhasePending, downloadv1alpha1.DownloadPhaseAssigned,
			downloadv1alpha1.DownloadPhaseQueued, downloadv1alpha1.DownloadPhaseDownloading,
			downloadv1alpha1.DownloadPhasePaused, downloadv1alpha1.DownloadPhaseCompleted,
			downloadv1alpha1.DownloadPhaseSeeding,
		} {
			set(downloadv1alpha1.DownloadPhaseFailed)
			settled(t, "reset", catalogv1alpha1.MoviePhaseWanted, "")
			set(p)
			settled(t, string(p), catalogv1alpha1.MoviePhaseDownloading, dl)
		}
		for _, p := range []downloadv1alpha1.DownloadPhase{
			downloadv1alpha1.DownloadPhaseImported, downloadv1alpha1.DownloadPhaseFailed,
			downloadv1alpha1.DownloadPhaseBlocklisted, downloadv1alpha1.DownloadPhaseRemoving,
		} {
			set(downloadv1alpha1.DownloadPhaseDownloading)
			settled(t, "reset", catalogv1alpha1.MoviePhaseDownloading, dl)
			set(p)
			settled(t, string(p), catalogv1alpha1.MoviePhaseWanted, "")
		}
	})

	// The two ways a Download must NOT become the ref: it belongs to a
	// different Movie of the same name (a deleted predecessor the garbage
	// collector has not reached), or it is terminal from the start.
	t.Run("a Download owned by another object, or already terminal, never sets the ref", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "sicario", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 273481, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		live := waitForPhase(t, ctx, c, "avail-ns", "sicario")

		// A stranger: same target name, owner UID of some other object.
		stranger := live.DeepCopy()
		stranger.UID = "00000000-0000-0000-0000-000000000000"
		grabPathDownload(t, ctx, c, stranger, "sicario-stranger01")

		terminal := grabPathDownload(t, ctx, c, &live, "sicario-failed0001")
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC(terminal, "avail-ns", downloadv1alpha1.DownloadPhaseFailed))
		require.NoError(t, err)

		require.Never(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "sicario"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef != nil
		}, time.Second, 50*time.Millisecond)
	})

	// The DLQ projector's annotation is folded into a DeadLettered
	// condition, on an object already in its steady state, and removing the
	// annotation removes the condition -- without releasing anything else
	// this manager owns.
	t.Run("the dead-lettered annotation folds into a DeadLettered condition and back out", func(t *testing.T) {
		before := waitForPhase(t, ctx, c, "avail-ns", "arrival")
		require.NotEmpty(t, before.Status.Phase)

		patch := client.MergeFrom(before.DeepCopy())
		if before.Annotations == nil {
			before.Annotations = map[string]string{}
		}
		before.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.catalogarr.search.high.x@2026-09-23T12:00:00Z"
		require.NoError(t, c.Patch(ctx, &before, patch))

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
		}, 5*time.Second, 20*time.Millisecond, "an annotation-only change must reach the reconcile and become a condition")
		assert.Equal(t, before.Status.Phase, got.Status.Phase, "folding the condition must not release the phase")
		assert.Equal(t, before.Status.ObservedGeneration, got.Status.ObservedGeneration)
		assert.True(t, got.Status.AddOptionsApplied)

		patch = client.MergeFrom(got.DeepCopy())
		delete(got.Annotations, k8s.AnnotationDeadLettered)
		require.NoError(t, c.Patch(ctx, &got, patch))
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
		}, 5*time.Second, 20*time.Millisecond, "removing the annotation must remove the condition")
	})

	// This is the concrete proof that the Movie predicate (GenerationChanged
	// Or StatusFieldChanged on status.metadata.refreshedAt) wakes this
	// controller when the metadata gateway writes status.metadata, but this
	// controller's own status write (which never touches status.metadata)
	// does not loop it. It runs last and in its own namespace so the
	// reconcileCounter delta, taken from a snapshot immediately before its
	// own writes, is not confused by any other subtest's residual work --
	// every prior subtest above settles (via require.Eventually) before this
	// one starts, and nothing further changes on their objects afterward.
	t.Run("metadata refresh triggers reconcile but the controller's own write does not loop", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "inception", Namespace: "selfloop-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 27205, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		// Wait for this object's own create-triggered reconcile to fully
		// settle (status.phase set) before taking the baseline snapshot,
		// rather than a blind "nothing incremented recently" check -- the
		// counter is shared across every object this controller has ever
		// touched, including the previous subtests' still-settling residue,
		// so a global quiet-period check is not a reliable signal that
		// *this* object's create pass is done.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "selfloop-ns", Name: "inception"}, &got); err != nil {
				return false
			}
			return got.Status.Phase != ""
		}, 5*time.Second, 20*time.Millisecond, "the initial create must reconcile to a settled phase")

		n := counter.count()

		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Inception").WithYear(2010).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool { return counter.count() > n }, 5*time.Second, 20*time.Millisecond,
			"the gateway's metadata write must wake this controller")

		// Let the controller's own reaction to the metadata write settle: it
		// patches Phase/Conditions/AddOptionsApplied/Available/Path, never
		// status.metadata, so it must not pass the predicate a second time.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "selfloop-ns", Name: "inception"}, &got); err != nil {
				return false
			}
			return got.Status.Phase != ""
		}, 5*time.Second, 20*time.Millisecond)

		n2 := counter.count()
		assert.Equal(t, int64(1), n2-n, "the gateway write must cause exactly one reconcile, not a cascade")

		require.Never(t, func() bool {
			return counter.count() > n2
		}, 500*time.Millisecond, 20*time.Millisecond,
			"this controller's own status patch must not re-trigger itself")
	})
}

// grabPathDownload creates a Download for owner the way the grab path does:
// one server-side apply under k8s.ManagerCatalogarrGrab, carrying an
// ownerReference to the Movie and nothing on the Movie itself. It returns
// the Download's name.
func grabPathDownload(t *testing.T, ctx context.Context, c client.Client, owner *catalogv1alpha1.Movie, name string) string {
	t.Helper()
	ref, err := k8s.OwnerReferenceAC(owner, k8s.MustNewScheme())
	require.NoError(t, err)
	dl := downloadac.Download(name, owner.Namespace).
		WithOwnerReferences(ref).
		WithSpec(downloadac.DownloadSpec().
			WithProtocol(commonv1.ProtocolTorrent).
			WithSource(downloadac.DownloadSource().WithMagnetURL("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")).
			WithRelease(commonv1.ReleaseInfo{
				GUID: "https://indexer.example/" + name, IndexerRef: "example", IndexerName: "Example",
				Title: "Fixture.2016.1080p", Protocol: commonv1.ProtocolTorrent,
				InfoHash: "0123456789abcdef0123456789abcdef01234567",
			}).
			WithTarget(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: owner.Name}))
	_, err = k8s.Apply(ctx, c, k8s.ManagerCatalogarrGrab, dl)
	require.NoError(t, err)
	return name
}

// statusFieldOwners returns the field managers whose status-subresource
// managedFields entry claims f:status.f:<field> on obj, sorted. This is the
// one place an over-claim is visible at all: pkg/k8s forces ownership, so a
// second writer never raises a conflict (CLAUDE.md, "A double-claim is
// silent").
func statusFieldOwners(t *testing.T, obj client.Object, field string) []string {
	t.Helper()
	var owners []string
	for _, e := range obj.GetManagedFields() {
		if e.FieldsV1 == nil {
			continue
		}
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		raw, ok := fields["f:status"]
		if !ok {
			continue
		}
		var status map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &status))
		if _, ok := status["f:"+field]; ok {
			owners = append(owners, e.Manager)
		}
	}
	sort.Strings(owners)
	return owners
}

// hasEvent reports whether an events.k8s.io/v1 Event with reason regards
// the named Movie -- what `kubectl describe movie` shows.
func hasEvent(ctx context.Context, c client.Client, ns, name, reason string) bool {
	var list eventsv1.EventList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false
	}
	for _, e := range list.Items {
		if e.Regarding.Kind == "Movie" && e.Regarding.Name == name && e.Reason == reason {
			return true
		}
	}
	return false
}

// newBareTestClient starts its own envtest apiserver and returns a plain
// (uncached) client, for scenarios whose Reconcile call never reaches a
// field-indexed List (only a manager's cache serves those -- see
// startManager's doc) and that must not be reconciled a second time by an
// auto-wired controller racing the test's own direct Reconcile call.
func newBareTestClient(t *testing.T) client.Client {
	t.Helper()
	cfg := newTestConfig(t)
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// TestMovieReconcilerQueueFull proves ErrQueueFull from Publish sets
// QueueFull=True and requeues after exactly one minute, without touching
// MetadataReady (§Step 6 queue full). It uses a bare, uncached client with
// no controller wired to it at all -- the QueueFull branch returns before
// reconcileNormal ever reaches the MediaFile field-indexed List, so no cache
// is needed, and running without one keeps this the only reconciler ever
// touching this object (no race with an auto-wired real-bus controller, and
// no SetupWithManager call to collide with TestMovieReconcilerRealController's).
func TestMovieReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("queuefull-ns")))

	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-2", Namespace: "queuefull-ns"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 950, QualityProfileRef: "none", RootFolderRef: "none"},
	}
	require.NoError(t, c.Create(ctx, m))

	r := &movie.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: fakePublisher{err: events.ErrQueueFull},
	}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "queuefull-ns", Name: "heat-2"}})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "queuefull-ns", Name: "heat-2"}, &got))
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionQueueFull)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

// TestMovieReconcilerTransientFailuresPreserveSteadyState is the review's
// mandatory regression case: TestMovieReconcilerQueueFull and
// TestMovieReconcilerAvailabilityAndPath's RootFolder coverage both start
// from a freshly-created object with no prior status, so there is nothing
// for a buggy early return to release -- this drives a Movie to a genuine
// steady state (Imported, with a file, quality, path and availability)
// FIRST, then triggers each transient failure and asserts every one of
// those fields survives. Without the reassertKnownStatus fix, both early
// returns build a status apply containing only
// ObservedGeneration/AddOptionsApplied/Conditions and PatchStatus releases
// everything else this manager previously sent -- a healthy Movie reset to
// zero by a blip in a metadata-queue publish or a momentary RootFolder
// lookup failure, with its conditions left describing the phase it no
// longer has.
func TestMovieReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("transient-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("transient-ns", "movies-root", "/data/media/movies")))
	require.NoError(t, c.Create(ctx, testQualityProfile("transient-ns", "cutoff-met-at-1080p", "Bluray-1080p")))

	driveToImported := func(t *testing.T, name string, tmdbID int64) reconcile.Request {
		t.Helper()
		bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}
		yesterday := metav1.NewTime(time.Now().Add(-24 * time.Hour))

		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "transient-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: tmdbID, QualityProfileRef: "cutoff-met-at-1080p", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityReleased,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Steady State").WithYear(2020).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithDigitalRelease(yesterday).WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		waitForCachedMetadata(t, ctx, c, "transient-ns", name)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "transient-ns", Name: name}}
		r := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		waitForPhase(t, ctx, c, "transient-ns", name)

		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-abc1234567", Namespace: "transient-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
				Path:     "/data/media/movies/Steady State (2020)/Steady State.mkv",
				Quality:  bluray,
			},
		}
		require.NoError(t, c.Create(ctx, mf))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.MediaFile
			return c.Get(ctx, types.NamespacedName{Namespace: "transient-ns", Name: mf.Name}, &got) == nil
		}, 5*time.Second, 10*time.Millisecond)

		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseImported
		}, 5*time.Second, 10*time.Millisecond, "setup: movie never reached Imported")
		require.True(t, got.Status.Available)
		require.NotNil(t, got.Status.FileRef)
		require.NotNil(t, got.Status.FileQuality)
		require.NotEmpty(t, got.Status.Path)
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToImported(t, "queuefull-steady", 9001)

		var before catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		// Force a fresh staleness decision on the next reconcile by backdating
		// status.metadata.refreshedAt well past the RefreshTTL, exactly as a
		// real metadata-gateway write would eventually require a new fetch.
		oldRefresh := metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))
		staleAC := catalogac.Movie(before.Name, before.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Steady State").WithYear(2020).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithDigitalRelease(metav1.NewTime(time.Now().Add(-24 * time.Hour))).
					WithRefreshedAt(oldRefresh),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, staleAC)
		require.NoError(t, err)
		// A tolerant "is it old now" check, not exact equality: the apiserver
		// round-trips metav1.Time through RFC 3339 at one-second precision,
		// so the in-memory oldRefresh (full Go time.Time precision) never
		// exactly equals what a subsequent Get reads back.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &movie.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: fakePublisher{err: events.ErrQueueFull},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// Same cache-staleness reasoning as elsewhere: poll for the
		// QueueFull condition before reading the rest of the object, rather
		// than a single Get immediately after Reconcile's own PatchStatus.
		var after catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, catalogv1alpha1.MovieConditionQueueFull)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, catalogv1alpha1.MoviePhaseImported, after.Status.Phase, "QueueFull must not release Phase")
		assert.True(t, after.Status.Available, "QueueFull must not release Available")
		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.True(t, after.Status.HasFile, "QueueFull must not release HasFile")
		require.NotNil(t, after.Status.FileRef)
		assert.Equal(t, *before.Status.FileRef, *after.Status.FileRef, "QueueFull must not release FileRef")
		require.NotNil(t, after.Status.FileQuality)
		assert.Equal(t, *before.Status.FileQuality, *after.Status.FileQuality, "QueueFull must not release FileQuality")
		assert.Equal(t, before.Status.FileFormatScore, after.Status.FileFormatScore, "QueueFull must not release FileFormatScore")
		assert.Equal(t, before.Status.CutoffMet, after.Status.CutoffMet, "QueueFull must not release CutoffMet")
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		req := driveToImported(t, "rootfolder-steady", 9002)

		var before catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		// A plain Update, not a merge patch: this main-resource write never
		// touches the status subresource (the CRD has one, so Update cannot
		// affect it even if it tried), and startCacheOnly has no auto-wired
		// controller racing this write, so there is no concurrent-writer
		// reason to prefer a patch here.
		var withMissingRoot catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, req.NamespacedName, &withMissingRoot))
		withMissingRoot.Spec.RootFolderRef = "does-not-exist"
		require.NoError(t, c.Update(ctx, &withMissingRoot))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// Same cache-staleness reasoning as elsewhere: poll for the
		// RootFolderNotFound reason before reading the rest of the object.
		var after catalogv1alpha1.Movie
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

		assert.Equal(t, catalogv1alpha1.MoviePhaseImported, after.Status.Phase, "RootFolderNotFound must not release Phase")
		assert.True(t, after.Status.Available, "RootFolderNotFound must not release Available")
		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.True(t, after.Status.HasFile, "RootFolderNotFound must not release HasFile")
		require.NotNil(t, after.Status.FileRef)
		assert.Equal(t, *before.Status.FileRef, *after.Status.FileRef, "RootFolderNotFound must not release FileRef")
		require.NotNil(t, after.Status.FileQuality)
		assert.Equal(t, *before.Status.FileQuality, *after.Status.FileQuality, "RootFolderNotFound must not release FileQuality")
		assert.Equal(t, before.Status.FileFormatScore, after.Status.FileFormatScore, "RootFolderNotFound must not release FileFormatScore")
		assert.Equal(t, before.Status.CutoffMet, after.Status.CutoffMet, "RootFolderNotFound must not release CutoffMet")
	})
}

// TestMovieReconcilerAvailabilityAndPath covers: with fresh cached metadata
// and a resolvable RootFolder, status.available/path/phase are computed end
// to end and RequeueAfter tracks availableAt (§Step 7). It uses
// startCacheOnly (a real cache, no auto-wired controller, no
// SetupWithManager call) and calls r.Reconcile directly so the RequeueAfter
// return value can be asserted precisely, without a second, auto-wired
// reconciler racing these newly-created objects' first finalizer-add.
func TestMovieReconcilerAvailabilityAndPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("avail-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("avail-ns", "movies-root", "/data/media/movies")))

	r := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}

	t.Run("available now", func(t *testing.T) {
		yesterday := metav1.NewTime(time.Now().Add(-24 * time.Hour))
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "inception", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 27205, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityReleased,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().
					WithTitle("Inception").WithYear(2010).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithDigitalRelease(yesterday).
					WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		waitForCachedMetadata(t, ctx, c, "avail-ns", "inception")

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "avail-ns", Name: "inception"}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		got := waitForPhase(t, ctx, c, "avail-ns", "inception")
		assert.True(t, got.Status.Available)
		assert.Equal(t, catalogv1alpha1.MoviePhaseWanted, got.Status.Phase)
		assert.Equal(t, "/data/media/movies/Inception (2010) [tmdbid-27205]", got.Status.Path)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MovieConditionAvailable)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})

	t.Run("available in the future requeues near availableAt", func(t *testing.T) {
		future := metav1.NewTime(time.Now().Add(72 * time.Hour))
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "dune-part-three", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 111, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityReleased,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().
					WithTitle("Dune Part Three").WithYear(2027).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithDigitalRelease(future).
					WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		waitForCachedMetadata(t, ctx, c, "avail-ns", "dune-part-three")

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "avail-ns", Name: "dune-part-three"}}
		res, err := r.Reconcile(ctx, req)
		require.NoError(t, err)

		got := waitForPhase(t, ctx, c, "avail-ns", "dune-part-three")
		assert.Equal(t, catalogv1alpha1.MoviePhaseUnavailable, got.Status.Phase)
		assert.InDelta(t, 72*time.Hour.Seconds(), res.RequeueAfter.Seconds(), 10)
	})

	t.Run("never available leaves RequeueAfter zero", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown-release", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 222, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityReleased,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		metaAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().
					WithTitle("Unknown Release").
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, metaAC)
		require.NoError(t, err)
		waitForCachedMetadata(t, ctx, c, "avail-ns", "unknown-release")

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "avail-ns", Name: "unknown-release"}}
		res, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Zero(t, res.RequeueAfter, "a zero availableAt must never feed RequeueAfter")

		got := waitForPhase(t, ctx, c, "avail-ns", "unknown-release")
		assert.False(t, got.Status.Available)
		assert.Equal(t, catalogv1alpha1.MoviePhaseUnavailable, got.Status.Phase)
	})
}

// recordingPublisher records every subject and envelope id it is asked to
// publish, in order.
type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
	ids      []string
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subject)
	p.ids = append(p.ids, e.ID)
	return events.Receipt{}, nil
}

// catalogSubjects returns the recorded clustarr.evt.catalog.* subjects with
// the trailing uid token cut off, in order, so a test can compare them
// without knowing the Movie's UID -- one per distinct envelope id, which is
// what the EVENTS stream keeps: a reconcile reading a cache that has not yet
// seen its own last apply re-observes the same edge and republishes the
// same id, and the stream's duplicate window drops the copy.
func (p *recordingPublisher) catalogSubjects() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	for i, s := range p.subjects {
		if !strings.HasPrefix(s, "clustarr.evt.catalog.") || seen[p.ids[i]] {
			continue
		}
		seen[p.ids[i]] = true
		out = append(out, s[:strings.LastIndex(s, ".")])
	}
	return out
}

// TestMovieReconcilerPublishesCatalogEvents drives one Movie through its
// whole life by direct Reconcile calls and asserts the catalog domain events
// it publishes, in order: added on the first reconcile, the file imported,
// updated on a spec edit, the file replaced by a newer one, the file
// deleted, and the Movie deleted. Each is published under one envelope id
// however many times the reconcile runs between edges, because the id is a
// function of the edge, not of the reconcile that saw it.
func TestMovieReconcilerPublishesCatalogEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "events-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "movies-root", "/data/media/movies")))
	require.NoError(t, c.Create(ctx, testQualityProfile(ns, "events-1080p", "Bluray-1080p")))

	pub := &recordingPublisher{}
	rec := k8sevents.NewFakeRecorder(64)
	r := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: rec, Bus: pub}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "heat"}}

	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 949, QualityProfileRef: "events-1080p", RootFolderRef: "movies-root",
			MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
		},
	}
	require.NoError(t, c.Create(ctx, m))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Movie(m.Name, ns).WithStatus(
		catalogac.MovieStatus().WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").WithYear(1995).
			WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	waitForCachedMetadata(t, ctx, c, ns, "heat")

	reconcileUntil := func(what string, cond func(catalogv1alpha1.Movie) bool) {
		t.Helper()
		require.Eventually(t, func() bool {
			if _, err := r.Reconcile(ctx, req); err != nil {
				return false
			}
			var got catalogv1alpha1.Movie
			return c.Get(ctx, req.NamespacedName, &got) == nil && cond(got)
		}, 10*time.Second, 50*time.Millisecond, what)
	}

	reconcileUntil("first reconcile", func(got catalogv1alpha1.Movie) bool { return got.Status.AddOptionsApplied })
	// A second pass over the same state announces nothing new.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, []string{"clustarr.evt.catalog.movie.added"}, pub.catalogSubjects())

	bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}
	first := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-first00001", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
			Path:     "/data/media/movies/Heat (1995)/Heat.mkv", Quality: bluray,
		},
	}
	require.NoError(t, c.Create(ctx, first))
	reconcileUntil("the first file", func(got catalogv1alpha1.Movie) bool {
		return got.Status.FileRef != nil && *got.Status.FileRef == first.Name
	})

	var live catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, req.NamespacedName, &live))
	patch := client.MergeFrom(live.DeepCopy())
	live.Spec.Tags = []string{"edited"}
	require.NoError(t, c.Patch(ctx, &live, patch))
	reconcileUntil("the spec edit", func(got catalogv1alpha1.Movie) bool {
		return got.Generation > 1 && got.Status.ObservedGeneration == got.Generation
	})

	// PickMediaFile takes the newest of the files flagged Original (both
	// are: spec.original defaults to true); creation timestamps have
	// one-second resolution, so wait one out.
	time.Sleep(1100 * time.Millisecond)
	second := first.DeepCopy()
	second.ObjectMeta = metav1.ObjectMeta{Name: "heat-second0001", Namespace: ns}
	second.Spec.Path = "/data/media/movies/Heat (1995)/Heat.2160p.mkv"
	require.NoError(t, c.Create(ctx, second))
	reconcileUntil("the replacement", func(got catalogv1alpha1.Movie) bool {
		return got.Status.FileRef != nil && *got.Status.FileRef == second.Name
	})

	require.NoError(t, c.Delete(ctx, first))
	require.NoError(t, c.Delete(ctx, second))
	reconcileUntil("the file removal", func(got catalogv1alpha1.Movie) bool { return got.Status.FileRef == nil })

	require.NoError(t, c.Delete(ctx, &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns}}))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Movie
		return c.Get(ctx, req.NamespacedName, &got) == nil && k8s.IsDeleting(&got)
	}, 5*time.Second, 20*time.Millisecond)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"clustarr.evt.catalog.movie.added",
		"clustarr.evt.catalog.mediafile.imported",
		"clustarr.evt.catalog.movie.updated",
		"clustarr.evt.catalog.mediafile.replaced",
		"clustarr.evt.catalog.mediafile.deleted",
		"clustarr.evt.catalog.movie.deleted",
	}, pub.catalogSubjects())

	// The recorder saw the phase edges, Wanted -> Imported and back.
	var notes []string
	for len(rec.Events) > 0 {
		notes = append(notes, <-rec.Events)
	}
	assert.Contains(t, notes, "Normal Imported phase Wanted -> Imported")
	assert.Contains(t, notes, "Normal Wanted phase Imported -> Wanted")
}
