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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestReaperReapsOrphanAfterDeleteWithNoWatchEvent is D2-8b's own test
// requirement: delete a Download while its transfer is live, WITHOUT letting
// the ordinary Reconcile/delete path run at all, and prove the transfer is
// gone from the client only once [Reaper] has seen it unmatched for a full
// grace period.
//
// It never calls Reconcile again after the delete -- that would exercise
// reconcileDeleting, not this package's answer to the case where that path
// never runs (the engine was down, the deletion landed during re-attach, or
// the watch event was simply missed). This test stands in for exactly that:
// a Download that is already gone from the apiserver, with a transfer this
// replica's [Engine.Client] still holds and nothing left to say the
// transfer should be there.
func TestReaperReapsOrphanAfterDeleteWithNoWatchEvent(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-reaper-orphan"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)

	reconciler := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}
	dl := mkTorrentDownload(t, ctx, c, ns, "movie-orphan", "torrents-0", "torrents", nil)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}

	// Establish a genuinely live transfer, matched to a real Download, the
	// same way production traffic would: through the normal Reconcile path.
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	id := got.Status.DownloadID
	require.NotEmpty(t, id)
	_, err = fc.Get(ctx, id)
	require.NoError(t, err, "the transfer must be live in the client before the orphan scenario starts")

	now := time.Now()
	reaper := &Reaper{
		Client:      c,
		Engine:      e,
		EngineID:    "torrents-0",
		OrphanGrace: 5 * time.Minute,
		Now:         func() time.Time { return now },
	}

	// Sanity: while the Download still exists and matches, a reap pass must
	// leave the transfer alone.
	require.NoError(t, reaper.ReapOnce(ctx))
	_, err = fc.Get(ctx, id)
	require.NoError(t, err, "a matched transfer must never be removed")

	// Delete the Download, then drop the engine finalizer the Reconcile
	// added without letting this engine run -- exactly what the Download
	// controller does on the engine's behalf once the engine has been gone
	// past its teardown timeout (ruling R-6), and the only way a transfer
	// outlives its Download now.
	require.Equal(t, []string{engine.Finalizer}, got.Finalizers)
	require.NoError(t, c.Delete(ctx, &got))
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	_, err = k8s.RemoveFinalizer(ctx, c, &got, engine.Finalizer)
	require.NoError(t, err)
	err = c.Get(ctx, req.NamespacedName, &downloadv1alpha1.Download{})
	require.True(t, apierrors.IsNotFound(err), "the Download must be fully gone, not merely marked for deletion")

	// First pass after the Download vanishes: the transfer is unmatched for
	// the first time. Guard two (the grace period) must hold even though
	// nothing will ever re-match it in this test -- the reaper cannot know
	// that in advance, which is the whole point of the guard.
	require.NoError(t, reaper.ReapOnce(ctx))
	_, err = fc.Get(ctx, id)
	require.NoError(t, err, "must not reap on the first unmatched sighting, even for a genuine orphan")

	// Cross the grace period and reap again.
	now = now.Add(6 * time.Minute)
	require.NoError(t, reaper.ReapOnce(ctx))

	_, err = fc.Get(ctx, id)
	assert.True(t, errors.Is(err, download.ErrNotFound),
		"the orphaned transfer must be removed from the client once unmatched continuously for the grace period")

	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, id, fc.removeCalls[0].id)
	assert.False(t, fc.removeCalls[0].deleteData,
		"the reaper must never pass deleteData=true: the Download is gone, so removeDataOnDelete cannot be read, and the controller's finalizer already handled disk state")
}

// TestOrphansDueWaitsForGracePeriod is a fast, in-memory proof of guard two
// on its own: an id absent from the known set is never returned as due until
// it has been seen unmatched, continuously, across a span at least as long
// as OrphanGrace.
func TestOrphansDueWaitsForGracePeriod(t *testing.T) {
	now := time.Now()
	r := &Reaper{OrphanGrace: time.Minute, Now: func() time.Time { return now }}
	items := []download.Item{{ID: "abc"}}
	known := map[string]struct{}{}

	assert.Empty(t, r.orphansDue(items, known), "must not reap on the first unmatched sighting")

	now = now.Add(30 * time.Second)
	assert.Empty(t, r.orphansDue(items, known), "must not reap before the grace period elapses")

	now = now.Add(31 * time.Second)
	assert.Equal(t, []string{"abc"}, r.orphansDue(items, known))
}

// TestOrphansDueResetsClockWhenMatchedAgain proves a transfer that flickers
// back to "matched" before the grace period elapses is never reaped on the
// strength of its earlier unmatched sighting -- the clock must restart from
// the next time it goes unmatched.
func TestOrphansDueResetsClockWhenMatchedAgain(t *testing.T) {
	now := time.Now()
	r := &Reaper{OrphanGrace: time.Minute, Now: func() time.Time { return now }}
	items := []download.Item{{ID: "abc"}}

	assert.Empty(t, r.orphansDue(items, map[string]struct{}{}))

	now = now.Add(2 * time.Minute)
	assert.Empty(t, r.orphansDue(items, map[string]struct{}{"abc": {}}),
		"a matched transfer is never due regardless of how long an earlier unmatched sighting aged")

	// Unmatched again immediately after being matched: this must count as a
	// FIRST sighting, not a continuation of the one from two minutes ago.
	assert.Empty(t, r.orphansDue(items, map[string]struct{}{}),
		"the clock must have reset by the matched pass; an immediate re-check must not reap")
}

// stubCacheSyncWaiter is a minimal [cacheSyncWaiter] test double: it signals
// that WaitForCacheSync was called, then blocks until ctx is done, mirroring
// how a real cache.Cache behaves during a graceful shutdown before it has
// ever synced.
type stubCacheSyncWaiter struct {
	called chan struct{}
}

func (s *stubCacheSyncWaiter) WaitForCacheSync(ctx context.Context) bool {
	close(s.called)
	<-ctx.Done()
	return false
}

// TestStartWaitsForCacheSyncBeforeTouchingTheClient proves [Reaper.Start]
// gates its whole loop on [Reaper.Cache]: with Client and Engine both nil
// (either would panic on first use), Start must still return cleanly once
// the context is cancelled while still waiting on the cache, because it
// never got past the wait to reach a tick.
func TestStartWaitsForCacheSyncBeforeTouchingTheClient(t *testing.T) {
	waiter := &stubCacheSyncWaiter{called: make(chan struct{})}
	r := &Reaper{
		// Client and Engine are deliberately nil: any code path that
		// dereferences them before the cache-sync gate opens panics loudly
		// rather than silently reaping against a cold cache.
		EngineID: "torrents-0",
		Cache:    waiter,
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
