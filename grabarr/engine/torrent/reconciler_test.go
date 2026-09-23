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

package torrent

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/engine"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestReconcileRefusesAllWorkBeforeReAttach is R4's own proof at the
// reconciler level: with Engine.Ready() false, Reconcile must not touch the
// download.Client at all -- no Add, no Get, nothing -- and must not even
// reach the Kubernetes API (the fake client below is nil and would panic on
// first use, which is deliberate: any code path past the gate fails loudly).
func TestReconcileRefusesAllWorkBeforeReAttach(t *testing.T) {
	fc := newFakeClient()
	r := &Reconciler{
		Client:   nil, // must never be dereferenced while the gate holds
		Engine:   &Engine{Client: fc, StateDir: t.TempDir()},
		EngineID: "torrents-0",
	}

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "not-ready-movie"},
	})
	require.NoError(t, err)
	assert.Equal(t, notReadyRequeue, res.RequeueAfter)
	assert.Equal(t, 0, fc.addCallCount(), "no Add call must happen before re-attach completes")

	// Now let re-attach complete (nothing persisted, so it finishes
	// immediately) and confirm the gate opens.
	_, err = r.Engine.ReAttach(context.Background())
	require.NoError(t, err)
	assert.True(t, r.Engine.Ready())
}

func newEnvtestClient(t *testing.T) client.Client {
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
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newTestNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})))
}

func mkTorrentDownload(t *testing.T, ctx context.Context, c client.Client, ns, name, engineID, clientRef string, mutate func(*downloadv1alpha1.Download)) *downloadv1alpha1.Download {
	t.Helper()
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{downloadv1alpha1.LabelEngine: engineID, downloadv1alpha1.LabelClient: clientRef},
		},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol:  commonv1alpha1.ProtocolTorrent,
			ClientRef: clientRef,
			Source:    downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1alpha1.ReleaseInfo{
				GUID: "https://indexer.example/1", IndexerRef: "example", IndexerName: "Example",
				Title: "Arrival.2016.1080p", Protocol: commonv1alpha1.ProtocolTorrent,
			},
			Target: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "arrival"},
		},
	}
	if mutate != nil {
		mutate(dl)
	}
	require.NoError(t, c.Create(ctx, dl))
	return dl
}

func mkTorrentDownloadClient(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 1,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
			Categories: map[string]string{
				string(commonv1alpha1.MediaKindMovie): "movies",
			},
		},
	}
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, dc)))
}

// TestReconcileAddThenSyncAppliesEngineTelemetry drives a Download from
// nothing through Add and one sync pass, using the in-memory fake client, and
// asserts the resulting status was written under k8s.ManagerGrabarrEngine
// with the fields the fake Item carries -- and none of the controller's.
func TestReconcileAddThenSyncAppliesEngineTelemetry(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-reconcile-add"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)

	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-a", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, defaultPollInterval, res.RequeueAfter)
	require.Equal(t, 1, fc.addCallCount())
	assert.Equal(t, "movies", fc.addRequests[0].Category, "category must come from DownloadClient.spec.categories[movie]")

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.NotEmpty(t, got.Status.DownloadID)
	assert.Equal(t, downloadv1alpha1.DownloadStageTransferring, got.Status.Stage)
	assert.Equal(t, []string{engine.Finalizer}, got.Finalizers,
		"the engine finalizer goes on with the first Add (ruling R-6), and it is the only one this package owns")

	// A second reconcile must not re-Add (idempotent id) and must sync
	// pause/seed-criteria/telemetry through the existing id.
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, defaultPollInterval, res.RequeueAfter)
	assert.Equal(t, 1, fc.addCallCount(), "the second reconcile must not Add again")
}

// TestReconcileSyncPausesAndResumesFollowingSpec proves spec.paused drives
// Pause/Resume through the client on subsequent reconciles.
func TestReconcileSyncPausesAndResumesFollowingSpec(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-reconcile-pause"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-b", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Flip spec.paused and reconcile again.
	require.NoError(t, c.Get(ctx, req.NamespacedName, dl))
	dl.Spec.Paused = true
	require.NoError(t, c.Update(ctx, dl))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, fc.pauseCalls, 1, "spec.paused=true must drive exactly one Pause call")

	// Flip back to running and confirm Resume follows.
	require.NoError(t, c.Get(ctx, req.NamespacedName, dl))
	dl.Spec.Paused = false
	require.NoError(t, c.Update(ctx, dl))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Len(t, fc.resumeCalls, 1, "spec.paused=false must drive exactly one Resume call")
}
