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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Gap fix Y2, the engine's half: a transfer's failure reaches status as the
// engine's report (engineFailureReason); once the controller's verdict is on
// the object the engine removes the transfer, data and descriptor with it,
// and never adds it back.
func TestReconcileReportsAFailureThenRemovesTheTransferOnceStopped(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-reconcile-stopped"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-stalled", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	id := got.Status.DownloadID
	require.NotEmpty(t, id)
	require.FileExists(t, sidecarFileName(e.StateDir, id))

	// The client fails the transfer; the next poll reports it.
	fc.fail(id, downloadv1alpha1.DownloadFailureStalled)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, got.Status.EngineFailureReason)
	assert.Empty(t, fc.removeCallsSnapshot(), "the engine must not act on its own observation")

	// The controller's verdict.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadac.Download(dl.Name, ns).WithStatus(
		downloadac.DownloadStatus().
			WithPhase(downloadv1alpha1.DownloadPhaseBlocklisted).
			WithFailureReason(downloadv1alpha1.DownloadFailureStalled)))
	require.NoError(t, err)

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "a stopped Download is not polled")
	calls := fc.removeCallsSnapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, removeCall{id: id, deleteData: true}, calls[0], "removeDataOnDelete defaults true")
	assert.NoFileExists(t, sidecarFileName(e.StateDir, id), "a restart must not re-attach a stopped transfer")

	// Level re-run: nothing more, and above all no Add.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Len(t, fc.removeCallsSnapshot(), 1)
	assert.Equal(t, 1, fc.addCallCount())

	// The engine's report outlives the transfer: nothing re-applied telemetry
	// without it.
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, got.Status.EngineFailureReason)
}

// A Download an operator labelled blocklisted before its engine reached it
// is never added.
func TestReconcileNeverAddsABlocklistLabelledDownload(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-reconcile-labelled"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-labelled", "torrents-0", "torrents", func(d *downloadv1alpha1.Download) {
		d.Labels[downloadv1alpha1.LabelBlocklisted] = downloadv1alpha1.LabelBlocklistedValue
	})
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)})
	require.NoError(t, err)
	assert.Zero(t, fc.addCallCount())
	assert.Empty(t, fc.removeCallsSnapshot())
}

func TestStallTimeoutResolvesTheCRDDefault(t *testing.T) {
	assert.Equal(t, downloadv1alpha1.DefaultStallTimeout, StallTimeout(nil))
	assert.Equal(t, downloadv1alpha1.DefaultStallTimeout, StallTimeout(&downloadv1alpha1.TorrentSpec{}))
	assert.Equal(t, 2*time.Hour, StallTimeout(&downloadv1alpha1.TorrentSpec{StallTimeout: &metav1.Duration{Duration: 2 * time.Hour}}))
	assert.Zero(t, StallTimeout(&downloadv1alpha1.TorrentSpec{StallTimeout: &metav1.Duration{}}), "0s disables")
}
