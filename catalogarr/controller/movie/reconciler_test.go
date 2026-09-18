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
	"log/slog"
	"os"
	"sync/atomic"
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
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
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
		Recorder:    mgr.GetEventRecorderFor("movie"), //nolint:staticcheck // matches C12's run.go registration line verbatim; record.EventRecorder is the brief's specified field type
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
// concurrently, and the loser gets a 409 conflict. The two field indices
// registered here must stay in sync with movie.Reconciler.SetupWithManager's
// own registration.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, ".spec.mediaRef.movie",
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Movie{}, ".status.activeDownloadRef",
		func(o client.Object) []string {
			m, ok := o.(*catalogv1alpha1.Movie)
			if !ok || m.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*m.Status.ActiveDownloadRef}
		}))

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

func strPtr(s string) *string { return &s }

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
	require.NoError(t, c.Create(ctx, testRootFolder("avail-ns", "movies-root", "/data/media/movies")))
	require.NoError(t, c.Create(ctx, testRootFolder("selfloop-ns", "movies-root", "/data/media/movies")))

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
		mediaKey := "metadata-ns/the-matrix"
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
	})

	// A watched Download rolls up Phase=Downloading and clears
	// ActiveDownloadRef on a terminal phase (§Step 22).
	t.Run("Download watch rolls up Downloading and clears the ref on a terminal phase", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "avail-ns"},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 329865, QualityProfileRef: "none", RootFolderRef: "movies-root",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}
		require.NoError(t, c.Create(ctx, m))
		waitForPhase(t, ctx, c, "avail-ns", "arrival")

		dl := &downloadv1alpha1.Download{
			ObjectMeta: metav1.ObjectMeta{Name: "arrival-abc1234567", Namespace: "avail-ns"},
			Spec: downloadv1alpha1.DownloadSpec{
				Protocol: commonv1.ProtocolTorrent,
				Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")},
				Release: commonv1.ReleaseInfo{
					GUID: "https://indexer.example/1", IndexerRef: "example", IndexerName: "Example",
					Title: "Arrival.2016.1080p", Protocol: commonv1.ProtocolTorrent,
					InfoHash: "0123456789abcdef0123456789abcdef01234567",
				},
				Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "arrival"},
			},
		}
		require.NoError(t, c.Create(ctx, dl))

		refAC := catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithActiveDownloadRef(dl.Name),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, refAC)
		require.NoError(t, err)
		// mapDownload's reverse lookup depends on the movieByActiveDownloadRef
		// field index, which is populated from the CACHE's view of
		// status.activeDownloadRef; wait for the cache to observe this write
		// before changing the Download's phase, or the Download's watch event
		// can fire before the index knows to map it back to "arrival" at all.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef != nil && *got.Status.ActiveDownloadRef == dl.Name
		}, 5*time.Second, 10*time.Millisecond)

		dlAC := downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseAssigned)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		var got catalogv1alpha1.Movie
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.MoviePhaseDownloading
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.ActiveDownloadRef)
		assert.Equal(t, dl.Name, *got.Status.ActiveDownloadRef)

		dlAC = downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseCompleted)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "avail-ns", Name: "arrival"}, &got); err != nil {
				return false
			}
			return got.Status.Phase != catalogv1alpha1.MoviePhaseDownloading
		}, 5*time.Second, 20*time.Millisecond)
		assert.Nil(t, got.Status.ActiveDownloadRef, "a terminal Download phase must clear the ref")
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
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10),
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
		r := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10), Bus: fakePublisher{}}
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
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10),
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

		r2 := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10), Bus: fakePublisher{}}
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

	r := &movie.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: record.NewFakeRecorder(10), Bus: bus}

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
