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

package issue_test

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/issue"
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

type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc()         { c.n.Add(1) }
func (c *reconcileCounter) count() int64 { return c.n.Load() }

// startManager wires a real issue.Reconciler into a real ctrl.Manager backed
// by the envtest apiserver, starts it, and waits for the cache to sync. Only
// one Test function in this package may call it, per controller-runtime's
// process-global controller-name registry -- the same constraint
// comic_test's startManager documents.
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	counter := &reconcileCounter{}
	r := &issue.Reconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("issue"), // matches run.go's registration line verbatim
		OnReconcile: counter.inc,
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

func downloadStatusAC(name, ns string, phase downloadv1alpha1.DownloadPhase) *downloadac.DownloadApplyConfiguration {
	return downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase))
}

func strPtr(s string) *string { return &s }

func testMediaFile(ns, name, issueName string, quality commonv1.Quality) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: issueName},
			Path:     "/data/media/comics/" + name + ".cbz",
			Quality:  quality,
		},
	}
}

// TestIssueReconcilerRealController is the one Test function that starts a
// real, auto-wired issue.Reconciler.
func TestIssueReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c, counter := startManager(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("issue-ns")))

	t.Run("finalizer add, HasFile from a watched MediaFile, State=Downloaded", func(t *testing.T) {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: "batman-001.0", Namespace: "issue-ns"},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: "batman", Number: "1", CalculatedNumberCentis: 100},
		}
		require.NoError(t, c.Create(ctx, iss))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Issue{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/issue", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-001.0"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			return hasFinalizer && got.Status.State == catalogv1alpha1.IssueStateWanted
		}, 5*time.Second, 20*time.Millisecond, "finalizer add and State=Wanted must both appear from the same reconcile pass")

		require.NoError(t, c.Create(ctx, testMediaFile("issue-ns", "batman-001-mf", "batman-001.0", commonv1.Quality{Name: "CBZ"})))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-001.0"}, &got); err != nil {
				return false
			}
			return got.Status.HasFile && got.Status.State == catalogv1alpha1.IssueStateDownloaded
		}, 5*time.Second, 20*time.Millisecond, "the MediaFile watch must flip HasFile and State")

		var got catalogv1alpha1.Issue
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-001.0"}, &got))
		require.NotNil(t, got.Status.FileRef)
		assert.Equal(t, "batman-001-mf", *got.Status.FileRef)
		require.NotNil(t, got.Status.FileQuality)
		assert.Equal(t, "CBZ", got.Status.FileQuality.Name)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.IssueConditionHasFile)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})

	t.Run("wakes on the Comic fan-out's own write of status.date, which bumps no generation", func(t *testing.T) {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: "hellboy-002.0", Namespace: "issue-ns"},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: "hellboy", Number: "2", CalculatedNumberCentis: 200},
		}
		require.NoError(t, c.Create(ctx, iss))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "hellboy-002.0"}, &got); err != nil {
				return false
			}
			return got.Status.State != ""
		}, 5*time.Second, 20*time.Millisecond, "the initial create must reconcile to a settled state")

		n := counter.count()

		future := metav1.NewTime(time.Now().Add(48 * time.Hour))
		fanoutAC := catalogac.Issue(iss.Name, iss.Namespace).WithStatus(
			catalogac.IssueStatus().WithSourceID("9999").WithTitle("The Corpse").WithDate(future),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrFanout, fanoutAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool { return counter.count() > n }, 5*time.Second, 20*time.Millisecond,
			"the Comic fan-out's status.date write must wake this controller")

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "hellboy-002.0"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.IssueConditionReleased)
			return cond != nil && cond.Status == metav1.ConditionFalse
		}, 5*time.Second, 20*time.Millisecond, "a future date must report Released=False")

		n2 := counter.count()
		assert.Equal(t, int64(1), n2-n, "the fan-out's write must cause exactly one reconcile, not a cascade")

		require.Never(t, func() bool {
			return counter.count() > n2
		}, 500*time.Millisecond, 20*time.Millisecond,
			"this controller's own status patch must not re-trigger itself")
	})

	// Download watch: State=Snatched while a Download is actively working,
	// ActiveDownloadRef cleared once it reaches a terminal phase -- the same
	// shape as episode/reconciler_test.go's "Download watch rolls up
	// Downloading and clears the ref on a terminal phase" subtest, adapted
	// to Issue's coarser State enum (no separate Delayed value; see
	// state.go's doc comment).
	t.Run("Download watch snatches and clears the ref on a terminal phase", func(t *testing.T) {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: "batman-003.0", Namespace: "issue-ns"},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: "batman", Number: "3", CalculatedNumberCentis: 300},
		}
		require.NoError(t, c.Create(ctx, iss))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-003.0"}, &got); err != nil {
				return false
			}
			return got.Status.State != ""
		}, 5*time.Second, 20*time.Millisecond)

		dl := &downloadv1alpha1.Download{
			ObjectMeta: metav1.ObjectMeta{Name: "batman-003-dl", Namespace: "issue-ns"},
			Spec: downloadv1alpha1.DownloadSpec{
				Protocol: commonv1.ProtocolTorrent,
				Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")},
				Release: commonv1.ReleaseInfo{
					GUID: "https://indexer.example/3", IndexerRef: "example", IndexerName: "Example",
					Title: "Batman.003.CBZ", Protocol: commonv1.ProtocolTorrent,
					InfoHash: "0123456789abcdef0123456789abcdef01234567",
				},
				Target: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "batman-003.0"},
			},
		}
		require.NoError(t, c.Create(ctx, dl))

		refAC := catalogac.Issue(iss.Name, iss.Namespace).WithStatus(
			catalogac.IssueStatus().WithActiveDownloadRef(dl.Name),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, refAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-003.0"}, &got); err != nil {
				return false
			}
			return got.Status.ActiveDownloadRef != nil && *got.Status.ActiveDownloadRef == dl.Name
		}, 5*time.Second, 10*time.Millisecond)

		dlAC := downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseDownloading)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		var got catalogv1alpha1.Issue
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-003.0"}, &got); err != nil {
				return false
			}
			return got.Status.State == catalogv1alpha1.IssueStateSnatched
		}, 5*time.Second, 20*time.Millisecond)
		require.NotNil(t, got.Status.ActiveDownloadRef)
		assert.Equal(t, dl.Name, *got.Status.ActiveDownloadRef)

		dlAC = downloadStatusAC(dl.Name, dl.Namespace, downloadv1alpha1.DownloadPhaseCompleted)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, dlAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			if err := c.Get(ctx, types.NamespacedName{Namespace: "issue-ns", Name: "batman-003.0"}, &got); err != nil {
				return false
			}
			return got.Status.State != catalogv1alpha1.IssueStateSnatched
		}, 5*time.Second, 20*time.Millisecond)
		assert.Nil(t, got.Status.ActiveDownloadRef, "a terminal Download phase must clear the ref")
		assert.Equal(t, catalogv1alpha1.IssueStateWanted, got.Status.State, "no file and no active download: back to Wanted")
	})
}
