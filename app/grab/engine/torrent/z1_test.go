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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The persisted seed record only grows, a met goal is written at once, and
// the rest is written at most once a seedPersistInterval.
func TestNextSeedRecord(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	_, due := nextSeedRecord(nil, download.Item{}, t0)
	assert.False(t, due, "nothing seeded yet: nothing to write")

	first, due := nextSeedRecord(nil, download.Item{UploadedBytes: 10, SeedTime: 90 * time.Second}, t0)
	require.True(t, due)
	assert.Equal(t, seedRecord{UploadedBytes: 10, SeedTimeSeconds: 90, SavedAt: t0}, first)

	_, due = nextSeedRecord(&first, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second}, t0.Add(30*time.Second))
	assert.False(t, due, "a counter change inside the interval waits")

	next, due := nextSeedRecord(&first, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second}, t0.Add(seedPersistInterval))
	require.True(t, due)
	assert.EqualValues(t, 20, next.UploadedBytes)

	met, due := nextSeedRecord(&next, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second, SeedGoalMet: true}, t0.Add(seedPersistInterval+time.Second))
	require.True(t, due, "a met goal is never deferred")
	assert.True(t, met.GoalMet)

	// A client re-verifying after a restart reports its goal unmet and its
	// counters lower until the check is done; recording that would lose the
	// very thing the record is for.
	kept, due := nextSeedRecord(&met, download.Item{}, t0.Add(time.Hour))
	assert.False(t, due)
	assert.True(t, kept.GoalMet)
	assert.EqualValues(t, 20, kept.UploadedBytes)
	assert.EqualValues(t, 100, kept.SeedTimeSeconds)

	h := met.history()
	require.NotNil(t, h)
	assert.Equal(t, download.SeedHistory{UploadedBytes: 20, SeedTime: 100 * time.Second, GoalMet: true}, *h)
	assert.Nil(t, (*seedRecord)(nil).history())
}

// An info hash other than spec.source.expectedInfoHash is reported as the
// engine's failure, payloadMismatch, instead of being retried forever; and
// the engine does not resolve the payload again while the controller's
// verdict is on its way.
func TestPayloadMismatchIsReportedNotRetried(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-payload-mismatch"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	fc.addErr = download.ErrPayloadMismatch
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "swapped", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err, "a payload mismatch is a report, not an error to back off and retry")
	assert.Zero(t, res.RequeueAfter)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.Equal(t, downloadv1alpha1.DownloadFailurePayloadMismatch, got.Status.EngineFailureReason)
	assert.Contains(t, got.Status.Message, "info hash")
	assert.Empty(t, got.Status.DownloadID, "nothing was added")
	assert.True(t, got.Status.EngineFailureReason.IsReleaseFault(), "the controller blocklists it")

	fc.addErr = nil
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, fc.addCallCount(), "the engine resolved and added the mismatched payload again")
}

// sync drives a spec.priority change to the client, persists the seed
// counters for a re-attach to hand back, and removes an imported torrent
// past its goal only when both the Download's removeOnImport and the
// client's removeCompleted allow it.
func TestSyncPriorityRemoveCompletedAndSeedRecord(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-z1-sync"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "seeded", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, dl))
	id := dl.Status.DownloadID
	require.NotEmpty(t, id)

	// spec.priority edited after the Add.
	dl.Spec.Priority = downloadv1alpha1.DownloadPriorityHigh
	require.NoError(t, c.Update(ctx, dl))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, fc.priorityCalls)
	assert.Equal(t, priorityCall{id: id, priority: downloadv1alpha1.DownloadPriorityHigh}, fc.priorityCalls[len(fc.priorityCalls)-1])

	// The torrent has seeded and met its goal; the client-wide switch is off.
	fc.mu.Lock()
	item := fc.items[id]
	item.UploadedBytes, item.SeedTime, item.SeedGoalMet = 3<<20, 2*time.Hour, true
	fc.items[id] = item
	fc.mu.Unlock()
	var dc downloadv1alpha1.DownloadClient
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "torrents"}, &dc))
	off := false
	dc.Spec.Torrent.RemoveCompleted = &off
	require.NoError(t, c.Update(ctx, &dc))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerImportarr,
		downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().
			WithImport(downloadac.ImportState().WithState(downloadv1alpha1.ImportPhaseImported))))
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, fc.removeCallsSnapshot(), "removeCompleted=false kept nothing on the engine")
	got, errs := loadDescriptors(e.StateDir)
	require.Empty(t, errs)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Desc.Seed, "the seed counters were not persisted")
	assert.True(t, got[0].Desc.Seed.GoalMet)
	assert.EqualValues(t, 3<<20, got[0].Desc.Seed.UploadedBytes)
	assert.EqualValues(t, 7200, got[0].Desc.Seed.SeedTimeSeconds)
	assert.Equal(t, downloadv1alpha1.DownloadPriorityHigh, got[0].Desc.Priority, "a priority change must survive a restart")

	// A restart hands the persisted seeding back to the client.
	fc2 := newFakeClient()
	_, err = (&Engine{Client: fc2, StateDir: e.StateDir}).ReAttach(ctx)
	require.NoError(t, err)
	require.Len(t, fc2.addRequests, 1)
	require.NotNil(t, fc2.addRequests[0].SeedHistory)
	assert.Equal(t, download.SeedHistory{UploadedBytes: 3 << 20, SeedTime: 2 * time.Hour, GoalMet: true},
		*fc2.addRequests[0].SeedHistory)
	assert.Equal(t, downloadv1alpha1.DownloadPriorityHigh, fc2.addRequests[0].Priority)

	// Turning the switch back on removes it.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "torrents"}, &dc))
	on := true
	dc.Spec.Torrent.RemoveCompleted = &on
	require.NoError(t, c.Update(ctx, &dc))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Len(t, fc.removeCallsSnapshot(), 1, "removeCompleted=true with removeOnImport=true removes the torrent")
}
