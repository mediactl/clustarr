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

package episode_test

import (
	"context"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/episode"
	"github.com/mediactl/clustarr/pkg/k8s"
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

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
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

func testSeries(ns, name, qualityProfileRef string) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 1, QualityProfileRef: qualityProfileRef, RootFolderRef: "none"},
	}
}

func downloadStatusAC(name, ns string, phase downloadv1alpha1.DownloadPhase) *downloadac.DownloadApplyConfiguration {
	return downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase))
}

func strPtr(s string) *string { return &s }

// startManager wires a real episode.Reconciler into a real ctrl.Manager
// backed by the envtest apiserver, starts it, and waits for the cache to
// sync. controller-runtime enforces controller-name uniqueness with a
// process-global registry (see the movie/series packages' identical note),
// so every scenario needing the real, auto-wired controller lives as a
// t.Run under one Test function that calls this exactly once.
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	r := &episode.Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("episode"), //nolint:staticcheck // matches C12's run.go registration line verbatim
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

func waitForPhase(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Episode {
	t.Helper()
	var got catalogv1alpha1.Episode
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

// TestEpisodeReconcilerRealController is the one Test function that starts
// a real, auto-wired episode.Reconciler (see startManager's doc for why
// there can only be one per test binary).
func TestEpisodeReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startManager(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("ep-ns")))

	t.Run("finalizer add does not early-return; Unaired then Wanted once the air date passes", func(t *testing.T) {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "the-expanse-s01e01", Namespace: "ep-ns"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "the-expanse", SeasonNumber: 1, EpisodeNumber: 1},
		}
		require.NoError(t, c.Create(ctx, ep))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Episode{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/episode", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-expanse-s01e01"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			return hasFinalizer && got.Status.Phase == catalogv1alpha1.EpisodePhaseUnaired
		}, 5*time.Second, 20*time.Millisecond,
			"finalizer and status.phase=Unaired must both appear from the same reconcile pass")

		got := waitForPhase(t, ctx, c, "ep-ns", "the-expanse-s01e01")
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.EpisodeConditionAired)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)

		// Simulate the Series reconciler's own write: provider fields set
		// under the distinct k8s.ManagerCatalogarrSeries (never
		// k8s.ManagerCatalogarr, which this reconciler uses for its own
		// computed fields -- see the package doc comment), including
		// AirDate in the past.
		yesterday := metav1.NewTime(time.Now().Add(-24 * time.Hour))
		provAC := catalogac.Episode("the-expanse-s01e01", "ep-ns").WithStatus(
			catalogac.EpisodeStatus().
				WithTitle("Dulcinea").WithOverview("The crew finds a ship.").
				WithTvdbID(123456).WithRuntimeMinutes(44).
				WithAirDate(yesterday),
		)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries, provAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-expanse-s01e01"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.EpisodePhaseWanted
		}, 5*time.Second, 20*time.Millisecond)

		got = waitForPhase(t, ctx, c, "ep-ns", "the-expanse-s01e01")
		cond = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.EpisodeConditionAired)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)

		// This reconciler's own repeated status patches must never clobber
		// the provider fields written under the same field manager --
		// the pass-through fix documented in reconciler.go.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-expanse-s01e01"}, &got); err != nil {
				return false
			}
			return got.Status.Title == "Dulcinea" && got.Status.Overview == "The crew finds a ship." &&
				got.Status.TvdbID == 123456 && got.Status.RuntimeMinutes == 44 &&
				got.Status.AirDate != nil && got.Status.Phase == catalogv1alpha1.EpisodePhaseWanted
		}, 2*time.Second, 20*time.Millisecond, "the episode reconciler's own patches must not clobber the Series-owned provider fields")
	})

	// The grab worker's status.pendingGrab write must both WAKE this
	// controller and be folded into Phase. Neither was true before: Phase
	// took no pendingGrab input, and episodePredicate fired on generation and
	// status.airDate only -- so a worker writing pendingGrab did not even
	// schedule a reconcile and the episode sat at Wanted for the whole delay
	// window.
	t.Run("a worker's pendingGrab write wakes this controller and reaches Delayed", func(t *testing.T) {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "delayed-s01e01", Namespace: "ep-ns"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "delayed-series", SeasonNumber: 1, EpisodeNumber: 1},
		}
		require.NoError(t, c.Create(ctx, ep))

		// Drive it to a settled Wanted first, or the assertion below could
		// not tell Delayed apart from "never reconciled".
		yesterday := metav1.NewTime(time.Now().Add(-24 * time.Hour))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries,
			catalogac.Episode(ep.Name, ep.Namespace).WithStatus(catalogac.EpisodeStatus().WithAirDate(yesterday)))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: ep.Name}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.EpisodePhaseWanted
		}, 10*time.Second, 20*time.Millisecond, "the episode must settle at Wanted before the delay is applied")

		// Exactly what catalogarr/worker/grab writes: pendingGrab only, under
		// the worker's own field manager, never Phase.
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
			catalogac.Episode(ep.Name, ep.Namespace).WithStatus(
				catalogac.EpisodeStatus().WithPendingGrab(
					catalogac.PendingGrab().
						WithReleaseTitle("Delayed.S01E01.1080p.WEB-DL-GROUP").
						WithProtocol(commonv1.ProtocolTorrent).
						WithGrabAt(metav1.NewTime(time.Now().Add(45*time.Minute))),
				),
			))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: ep.Name}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.EpisodePhaseDelayed
		}, 10*time.Second, 20*time.Millisecond,
			"the pendingGrab write must wake this controller and recompute Phase=Delayed")

		// And the Series-owned provider field it never touched survives.
		var got catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: ep.Name}, &got))
		assert.NotNil(t, got.Status.AirDate)
	})

	t.Run("MediaFile watch rolls up HasFile and reaches Imported or CutoffUnmet", func(t *testing.T) {
		bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}

		require.NoError(t, c.Create(ctx, testSeries("ep-ns", "the-wire", "cutoff-met-at-1080p")))
		require.NoError(t, c.Create(ctx, testQualityProfile("ep-ns", "cutoff-met-at-1080p", "Bluray-1080p")))
		require.NoError(t, c.Create(ctx, testQualityProfile("ep-ns", "cutoff-is-4k-remux", "Remux-2160p")))

		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "the-wire-s01e01", Namespace: "ep-ns"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "the-wire", SeasonNumber: 1, EpisodeNumber: 1},
		}
		require.NoError(t, c.Create(ctx, ep))
		waitForPhase(t, ctx, c, "ep-ns", "the-wire-s01e01")

		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "the-wire-s01e01-abc1234567", Namespace: "ep-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
				Path:     "/data/media/tv/The Wire/Season 01/The Wire - S01E01.mkv",
				Quality:  bluray,
			},
		}
		require.NoError(t, c.Create(ctx, mf))

		var got catalogv1alpha1.Episode
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e01"}, &got); err != nil {
				return false
			}
			return got.Status.HasFile
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.FileRef)
		assert.Equal(t, "the-wire-s01e01-abc1234567", *got.Status.FileRef)
		assert.Equal(t, catalogv1alpha1.EpisodePhaseImported, got.Status.Phase)

		// A stricter profile (cutoff at 4k remux) reaches CutoffUnmet instead.
		var series catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire"}, &series))
		patch := client.MergeFrom(series.DeepCopy())
		series.Spec.QualityProfileRef = "cutoff-is-4k-remux"
		require.NoError(t, c.Patch(ctx, &series, patch))

		// The Series' own profile change does not itself re-trigger this
		// Episode's reconcile (Episode only watches MediaFile/Download and
		// its own spec/airDate) -- bump the MediaFile's own generation
		// (Path is a mutable spec field, unlike the immutable MediaRef) to
		// fire mapMediaFile and force a re-evaluation. EpisodeSpec has no
		// harmless field to bump instead: SeriesRef/SeasonNumber/
		// EpisodeNumber are all immutable, and Monitored feeds Phase
		// directly, so flipping it would change the very phase under test.
		var gotMF catalogv1alpha1.MediaFile
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e01-abc1234567"}, &gotMF))
		mfPatch := client.MergeFrom(gotMF.DeepCopy())
		gotMF.Spec.Path = "/data/media/tv/The Wire/Season 01/The Wire - S01E01 (renamed).mkv"
		require.NoError(t, c.Patch(ctx, &gotMF, mfPatch))

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e01"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.EpisodePhaseCutoffUnmet
		}, 5*time.Second, 20*time.Millisecond)
	})

	t.Run("Download watch rolls up Downloading and clears the ref on a terminal phase", func(t *testing.T) {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "the-wire-s01e02", Namespace: "ep-ns"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "the-wire", SeasonNumber: 1, EpisodeNumber: 2},
		}
		require.NoError(t, c.Create(ctx, ep))
		waitForPhase(t, ctx, c, "ep-ns", "the-wire-s01e02")

		dl := &downloadv1alpha1.Download{
			ObjectMeta: metav1.ObjectMeta{Name: "the-wire-s01e02-abc1234567", Namespace: "ep-ns"},
			Spec: downloadv1alpha1.DownloadSpec{
				Protocol: commonv1.ProtocolTorrent,
				Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")},
				Release: commonv1.ReleaseInfo{
					GUID: "https://indexer.example/2", IndexerRef: "example", IndexerName: "Example",
					Title: "The.Wire.S01E02.1080p", Protocol: commonv1.ProtocolTorrent,
					InfoHash: "0123456789abcdef0123456789abcdef01234567",
				},
				Target: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e02"},
			},
		}
		require.NoError(t, c.Create(ctx, dl))

		refAC := catalogac.Episode(ep.Name, ep.Namespace).WithStatus(
			catalogac.EpisodeStatus().WithActiveDownloadRef(dl.Name),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, refAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e02"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef != nil && *got.Status.ActiveDownloadRef == dl.Name
		}, 5*time.Second, 10*time.Millisecond)

		dlAC := downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseAssigned)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		var got catalogv1alpha1.Episode
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e02"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.EpisodePhaseDownloading
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.ActiveDownloadRef)
		assert.Equal(t, dl.Name, *got.Status.ActiveDownloadRef)

		dlAC = downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseCompleted)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ep-ns", Name: "the-wire-s01e02"}, &got); err != nil {
				return false
			}
			return got.Status.Phase != catalogv1alpha1.EpisodePhaseDownloading
		}, 5*time.Second, 20*time.Millisecond)
		assert.Nil(t, got.Status.ActiveDownloadRef, "a terminal Download phase must clear the ref")
	})
}
