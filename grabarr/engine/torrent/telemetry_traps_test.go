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
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestApplyTelemetryTwicePublishesFilesOnceNotTwice is the first trap: Files
// is a listType=map keyed on path, and EngineFields/download.ApplyStatus
// calls WithFiles in a loop. That is only safe because ApplyStatus always
// starts from a FRESH apply configuration (downloadac.DownloadStatus()); a
// mutate that instead started from a SEEDED one and called WithFiles again
// would double every entry. This proves applyTelemetry -- which always goes
// through download.ApplyStatus, never a seeded ac -- does not do that across
// repeated polls.
func TestApplyTelemetryTwicePublishesFilesOnceNotTwice(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-telemetry-files"
	newTestNamespace(t, ctx, c, ns)

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-files", "torrents-0", "torrents", nil)
	key := client.ObjectKeyFromObject(dl)

	r := &Reconciler{Client: c}
	item := download.Item{
		ID:          "0123456789abcdef0123456789abcdef01234567",
		ContentRoot: "/data/torrents/movies/movie-files",
		Files: []download.File{
			{Path: "movie-files.mkv", SizeBytes: 8 << 30},
			{Path: "sample.mkv", SizeBytes: 1 << 20, Skipped: true},
		},
	}

	require.NoError(t, r.applyTelemetry(ctx, key, item))
	require.NoError(t, r.applyTelemetry(ctx, key, item))

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))
	assert.Len(t, got.Status.Files, 2, "two applies of the SAME file list must not double the entries")
}

// TestApplyTelemetryDoesNotClobberAConcurrentControllerWrite is the second
// trap, empirically: it holds a Download object read BEFORE a concurrent
// controller write (simulating ManagerGrabarr setting phase mid-reconcile),
// performs applyTelemetry from that same stale snapshot's key, and asserts
// the controller's concurrently-written field survives. applyTelemetry is
// immune to this by construction (its apply configuration comes entirely
// from a live download.Item, never from the Download it re-Gets), but the
// test proves the empirical outcome rather than trusting the argument: no
// release-regression test in this tree can see a LOST UPDATE by asserting
// values alone if the writer never seeds from the stale read in the first
// place -- which is exactly what this test demonstrates holds here.
func TestApplyTelemetryDoesNotClobberAConcurrentControllerWrite(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-telemetry-lostupdate"
	newTestNamespace(t, ctx, c, ns)

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-lu", "torrents-0", "torrents", nil)
	key := client.ObjectKeyFromObject(dl)

	// A stale read, taken before the "slow work" and before the concurrent
	// controller write below -- exactly the shape CLAUDE.md's lost-update
	// hazard describes.
	var stale downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &stale))

	// The controller (ManagerGrabarr) writes phase concurrently, "while" this
	// engine's slow work (an HTTP resolve, in the real path) is in flight.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerGrabarr, &stale,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithPhase(downloadv1alpha1.DownloadPhaseDownloading).
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: downloadv1alpha1.DownloadConditionAssigned, Status: metav1.ConditionTrue,
					Reason: "Assigned", LastTransitionTime: metav1.Now(),
				}))
		}))

	// This engine now applies telemetry, using the SAME (now stale) key --
	// applyTelemetry re-Gets internally, but the point under test is that
	// even the CONTENT of its apply configuration never depended on `stale`.
	r := &Reconciler{Client: c}
	item := download.Item{ID: "0123456789abcdef0123456789abcdef01234567", DownRate: 12_000_000}
	require.NoError(t, r.applyTelemetry(ctx, key, item))

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))
	assert.Equal(t, downloadv1alpha1.DownloadPhaseDownloading, got.Status.Phase,
		"the engine's telemetry apply must not roll back the controller's concurrently-written phase")
	assert.NotEmpty(t, got.Status.Conditions, "the engine's telemetry apply must not roll back the controller's conditions")
	assert.EqualValues(t, 12_000_000, got.Status.DownloadRateBps, "the engine's own field must still land")
}

// TestApplyTelemetryManagedFieldsAreOnlyGrabarrEngine is the third trap:
// assert on metadata.managedFields, not on values, because a double-claim
// under ForceOwnership is invisible to a value assertion. This mirrors
// grabarr/controller/downloadclient/managedfields_envtest_test.go's pattern,
// exercised through THIS package's own applyTelemetry rather than
// grabarr/status directly -- catching a wiring mistake here (the wrong
// constant, a typo) that grabarr/status's own tests cannot see because they
// call status.Patch with the right manager by construction.
func TestApplyTelemetryManagedFieldsAreOnlyGrabarrEngine(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-telemetry-managedfields"
	newTestNamespace(t, ctx, c, ns)

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-mf", "torrents-0", "torrents", nil)
	key := client.ObjectKeyFromObject(dl)

	r := &Reconciler{Client: c}
	item := download.Item{
		ID: "0123456789abcdef0123456789abcdef01234567",
		Files: []download.File{
			{Path: "movie-mf.mkv", SizeBytes: 1 << 30},
		},
		DownloadedBytes: 1 << 30,
		Seeders:         3,
	}
	require.NoError(t, r.applyTelemetry(ctx, key, item))

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))

	found := false
	for _, entry := range got.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		require.Equalf(t, k8s.ManagerGrabarrEngine.String(), entry.Manager,
			"only k8s.ManagerGrabarrEngine may own status fields after this package's own apply, got %q", entry.Manager)
		found = true

		var fields map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields))
		st, ok := fields["f:status"].(map[string]any)
		require.True(t, ok)
		var names []string
		for k := range st {
			names = append(names, strings.TrimPrefix(k, "f:"))
		}
		sort.Strings(names)
		assert.Contains(t, names, "downloadID")
		assert.Contains(t, names, "files")
		assert.Contains(t, names, "downloadedBytes")
		assert.NotContains(t, names, "phase", "the engine must never claim status.phase")
		assert.NotContains(t, names, "conditions", "the engine must never claim status.conditions")
	}
	assert.True(t, found, "the apply must have produced a status managedFields entry")
}

// TestReconcileDeletingRemovesDataWhenRequestedAndTouchesNoOtherManagedField
// mirrors grabarr/engine/usenet's identical test
// (TestReconcileDeletingRemovesTransferAndTouchesNoOtherManagedField). A
// FOREIGN finalizer (standing in for the Download controller's own) keeps
// the object around once this engine has dropped its [engine.Finalizer],
// so the test can look at what reconcileDeleting left behind: the transfer
// removed, the engine finalizer gone and the foreign one untouched, and no
// status write or managedFields entry of its own.
func TestReconcileDeletingRemovesDataWhenRequestedAndTouchesNoOtherManagedField(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-delete-data"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	removeData := true
	dl := mkTorrentDownload(t, ctx, c, ns, "movie-del", "torrents-0", "torrents", func(d *downloadv1alpha1.Download) {
		d.Spec.RemoveDataOnDelete = &removeData
		d.Finalizers = []string{"test.clustarr.io/keep"}
	})
	key := client.ObjectKeyFromObject(dl)

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	require.Equal(t, 1, fc.addCallCount())

	require.NoError(t, c.Get(ctx, key, dl))
	require.NoError(t, c.Delete(ctx, dl))

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	require.Len(t, fc.removeCalls, 1)
	assert.True(t, fc.removeCalls[0].deleteData)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got), "the object must still exist -- the foreign finalizer holds it")
	assert.Equal(t, []string{"test.clustarr.io/keep"}, got.Finalizers,
		"the engine finalizer must be dropped once the transfer is removed, and nothing else touched")
	assert.NotEmpty(t, got.Status.DownloadID, "reconcileDeleting makes no status write of its own")

	statusManagers := map[string]bool{}
	for _, entry := range got.ManagedFields {
		if entry.Subresource == "status" {
			statusManagers[entry.Manager] = true
		}
	}
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarrEngine.String(): true}, statusManagers,
		"reconcileDeleting must add no managedFields entry of its own")
}

// TestReconcileDeletingKeepsDataWhenNotRequested proves the inverse: a false
// RemoveDataOnDelete must still remove the transfer from the client (it is
// leaving the engine's management either way) but pass deleteData=false.
func TestReconcileDeletingKeepsDataWhenNotRequested(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-delete-keep"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	removeData := false
	dl := mkTorrentDownload(t, ctx, c, ns, "movie-keep", "torrents-0", "torrents", func(d *downloadv1alpha1.Download) {
		d.Spec.RemoveDataOnDelete = &removeData
		d.Finalizers = []string{"test.clustarr.io/keep"}
	})
	key := client.ObjectKeyFromObject(dl)

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, key, dl))
	require.NoError(t, c.Delete(ctx, dl))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	require.Len(t, fc.removeCalls, 1)
	assert.False(t, fc.removeCalls[0].deleteData)
}
