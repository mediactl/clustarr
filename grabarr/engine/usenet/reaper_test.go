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

package usenet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/grabarr/engine/usenet"
	grabarrstatus "github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestReaperReapsOrphanAfterDeleteWithNoWatchEvent is D2-8b's own test
// requirement: delete a Download while its transfer is live, WITHOUT letting
// the ordinary Reconcile/delete path run at all, and prove the transfer is
// gone from the client only once [usenetengine.Reaper] has seen it unmatched
// for a full grace period.
//
// It deliberately never calls Reconcile after the delete -- that would
// exercise [usenetengine.Reconciler]'s own reconcileDeleting, which this
// package already covers in engine_envtest_test.go's
// TestReconcileDeletingRemovesTransferAndTouchesNoOtherManagedField (a
// finalizer keeps that object alive so the watch fires). This test is the
// case where the watch never fires at all: the Download is deleted with no
// finalizer, is fully gone from the apiserver by the time anything looks
// again, and the only thing standing between that and a transfer running
// forever is the level-driven Reaper.
func TestReaperReapsOrphanAfterDeleteWithNoWatchEvent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-orphan", "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.setItem(download.Item{ID: "live-orphan", Status: download.StatusDownloading, TotalBytes: 1000, DownloadedBytes: 100})

	// Record the id on the Download the same way a real engine reconcile
	// would (grabarrstatus.Patch under ManagerGrabarrEngine), so the
	// transfer starts out genuinely matched.
	key := types.NamespacedName{Namespace: "default", Name: "movie-orphan"}
	require.NoError(t, c.Get(ctx, key, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithDownloadID("live-orphan") }))

	now := time.Now()
	reaper := &usenetengine.Reaper{
		Client:      c,
		Download:    fc,
		Engine:      "sabnzbd-0",
		OrphanGrace: 5 * time.Minute,
		Now:         func() time.Time { return now },
	}

	// Sanity: while the Download still exists and matches, a reap pass must
	// leave the transfer alone.
	require.NoError(t, reaper.ReapOnce(ctx))
	_, err := fc.Get(ctx, "live-orphan")
	require.NoError(t, err, "a matched transfer must never be removed")

	// Delete the Download directly. No finalizer is set (newUsenetDownload
	// adds none, and this package never claims one -- see doc.go), so it is
	// gone from the apiserver immediately, exactly as it would be once
	// grabarr/controller/download's finalizer has already run
	// fsops.SafeRemove and dropped its own finalizer without waiting for
	// this engine.
	require.NoError(t, c.Get(ctx, key, dl))
	require.NoError(t, c.Delete(ctx, dl))
	err = c.Get(ctx, key, &downloadv1alpha1.Download{})
	require.True(t, apierrors.IsNotFound(err), "the Download must be fully gone, not merely marked for deletion")

	// First pass after the Download vanishes: the transfer is unmatched for
	// the first time. The grace-period guard must hold even though nothing
	// will ever re-match it in this test -- the reaper cannot know that in
	// advance, which is the whole point of the guard.
	require.NoError(t, reaper.ReapOnce(ctx))
	_, err = fc.Get(ctx, "live-orphan")
	require.NoError(t, err, "must not reap on the first unmatched sighting, even for a genuine orphan")

	// Cross the grace period and reap again.
	now = now.Add(6 * time.Minute)
	require.NoError(t, reaper.ReapOnce(ctx))

	_, err = fc.Get(ctx, "live-orphan")
	assert.True(t, errors.Is(err, download.ErrNotFound),
		"the orphaned transfer must be removed from the client once unmatched continuously for the grace period")

	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, "live-orphan", fc.removeCalls[0].id)
	assert.False(t, fc.removeCalls[0].deleteData,
		"the reaper must never pass deleteData=true: the Download is gone, so removeDataOnDelete cannot be read, and the controller's finalizer already handled disk state")
}

// stubCacheSyncWaiter is a minimal cacheSyncWaiter test double: it signals
// that WaitForCacheSync was called, then blocks until ctx is done, mirroring
// how a real cache.Cache behaves during a graceful shutdown before it has
// ever synced. It satisfies usenetengine.Reaper.Cache structurally --
// nothing in this package needs to name the (unexported) interface type.
type stubCacheSyncWaiter struct {
	called chan struct{}
}

func (s *stubCacheSyncWaiter) WaitForCacheSync(ctx context.Context) bool {
	close(s.called)
	<-ctx.Done()
	return false
}

// TestStartWaitsForCacheSyncBeforeTouchingTheClient proves
// [usenetengine.Reaper.Start] gates its whole loop on [usenetengine.Reaper.Cache]:
// with Client and Download both nil (either would panic on first use), Start
// must still return cleanly once the context is cancelled while still
// waiting on the cache, because it never got past the wait to reach a tick.
func TestStartWaitsForCacheSyncBeforeTouchingTheClient(t *testing.T) {
	waiter := &stubCacheSyncWaiter{called: make(chan struct{})}
	r := &usenetengine.Reaper{
		// Client and Download are deliberately nil: any code path that
		// dereferences them before the cache-sync gate opens panics loudly
		// rather than silently reaping against a cold cache.
		Engine: "sabnzbd-0",
		Cache:  waiter,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	select {
	case <-waiter.called:
	case <-time.After(2 * time.Second):
		t.Fatal("Start never called WaitForCacheSync")
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "Start must return nil on a cancelled context, matching every other Runnable in this tree")
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after the cache sync wait was cancelled")
	}
}
