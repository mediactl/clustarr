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

package grab

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// allHoldersActive is the holder check for tests about contention alone:
// every existing lease is someone else's live grab.
func allHoldersActive(context.Context, events.Entry) (holderState, error) { return holderActive, nil }

func packLeaseKeys(names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, events.LeaseKey(MediaKey("media", mediaRefEpisode(n))))
	}
	return out
}

func TestAcquireLeases_AllOrNothingAcrossPackEpisodes(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01", "the-wire-s01e02", "the-wire-s01e03")

	// An earlier, unrelated grab already holds the middle episode.
	_, err := kv.Create(ctx, keys[1], []byte("some-other-download"))
	require.NoError(t, err)

	acquired, err := acquireLeases(ctx, kv, keys, "download-a", allHoldersActive)
	require.ErrorIs(t, err, ErrDuplicateGrab)
	assert.Empty(t, acquired, "acquireLeases must return nothing on partial failure")

	// The lease it DID take (episode 1, before it hit episode 2) must have
	// been rolled back, not left dangling for the sweeper.
	_, err = kv.Get(ctx, keys[0])
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "episode 1's lease should have been rolled back")

	// The pre-existing lease is untouched: rollback deletes only what this
	// call created.
	entry, err := kv.Get(ctx, keys[1])
	require.NoError(t, err)
	assert.Equal(t, "some-other-download", string(entry.Value))
}

// TestAcquireLeases_TwoWorkersOneDownload is spec §14's grab-lease race: two
// goroutines contend for the same keys and exactly one wins. It is the reason
// the lease is Create-fails-if-exists rather than a read-then-write.
func TestAcquireLeases_TwoWorkersOneDownload(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01", "the-wire-s01e02")

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"download-a", "download-b"} {
		wg.Add(1)
		go func(downloadName string) {
			defer wg.Done()
			<-start
			_, err := acquireLeases(ctx, kv, keys, downloadName, allHoldersActive)
			results <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)

	var wins, dups int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case assert.ErrorIs(t, err, ErrDuplicateGrab):
			dups++
		}
	}
	assert.Equal(t, 1, wins, "exactly one worker may win the race")
	assert.Equal(t, 1, dups, "the loser must see ErrDuplicateGrab")

	// Both keys belong to the winner, and neither was collaterally rolled
	// back by the loser's cleanup.
	var holder string
	for i, k := range keys {
		entry, err := kv.Get(ctx, k)
		require.NoErrorf(t, err, "lease %s missing after the race", k)
		if i == 0 {
			holder = string(entry.Value)
		}
		assert.Equal(t, holder, string(entry.Value), "the pack's leases must all be held by one download")
	}
}

func TestReleaseLeases_IsBestEffortAndIdempotent(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01")

	acquired, err := acquireLeases(ctx, kv, keys, "download-a", allHoldersActive)
	require.NoError(t, err)
	releaseLeases(ctx, kv, acquired)
	releaseLeases(ctx, kv, acquired) // deleting an absent key is not an error

	_, err = kv.Get(ctx, keys[0])
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}

// TestAcquireLeases_ReentersItsOwnLease is the redelivery of a grab that
// created its Download and then failed before publishing: the lease already
// names this Download, so the grab must re-enter it rather than report itself
// as its own duplicate. A re-entered lease is not "taken" -- a rollback of
// this call must leave it where the earlier delivery put it.
func TestAcquireLeases_ReentersItsOwnLease(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01", "the-wire-s01e02")
	_, err := kv.Create(ctx, keys[0], []byte("download-a"))
	require.NoError(t, err)

	holderAsked := false
	taken, err := acquireLeases(ctx, kv, keys, "download-a", func(context.Context, events.Entry) (holderState, error) {
		holderAsked = true
		return holderActive, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{keys[1]}, taken, "only the key this call created is its to roll back")
	assert.False(t, holderAsked, "a lease held under this grab's own name needs no holder check")
}

// TestAcquireLeases_ReclaimsAStaleLease is what lets an item be grabbed a
// second time at all. Nothing deletes a lease when its Download finishes, so
// without a takeover the first grab's lease blocked every later one -- the
// retry after a failed download, the upgrade -- as a "duplicate" forever.
func TestAcquireLeases_ReclaimsAStaleLease(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01")
	_, err := kv.Create(ctx, keys[0], []byte("old-imported-download"))
	require.NoError(t, err)

	var asked string
	taken, err := acquireLeases(ctx, kv, keys, "download-b", func(_ context.Context, e events.Entry) (holderState, error) {
		asked = string(e.Value)
		return holderStale, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "old-imported-download", asked, "the holder check is asked about the Download the lease names")
	assert.Equal(t, keys, taken, "a reclaimed lease is this call's to roll back")

	entry, err := kv.Get(ctx, keys[0])
	require.NoError(t, err)
	assert.Equal(t, "download-b", string(entry.Value))
}

// TestAcquireLeases_ActiveHolderIsADuplicateAndRollsBack: a live holder on any
// key of a pack refuses the whole pack, and the keys this call had already
// taken -- created or reclaimed -- are rolled back.
func TestAcquireLeases_ActiveHolderIsADuplicateAndRollsBack(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01", "the-wire-s01e02", "the-wire-s01e03")
	_, err := kv.Create(ctx, keys[0], []byte("stale-download"))
	require.NoError(t, err)
	_, err = kv.Create(ctx, keys[2], []byte("live-download"))
	require.NoError(t, err)

	_, err = acquireLeases(ctx, kv, keys, "download-c", func(_ context.Context, e events.Entry) (holderState, error) {
		if string(e.Value) == "live-download" {
			return holderActive, nil
		}
		return holderStale, nil
	})
	require.ErrorIs(t, err, ErrDuplicateGrab)

	_, err = kv.Get(ctx, keys[0])
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the reclaimed lease is rolled back with the rest")
	_, err = kv.Get(ctx, keys[1])
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the created lease is rolled back")
	entry, err := kv.Get(ctx, keys[2])
	require.NoError(t, err)
	assert.Equal(t, "live-download", string(entry.Value), "the live holder's lease is untouched")
}

// TestFreeLeases_DeletesOnlyTheFailedDownloadsKeys is spec §8.3's lease
// delete: a season pack frees every episode lease its Download holds, and
// leaves alone the one a later grab already reclaimed and the one nobody
// held -- so a redelivered failure frees nothing twice.
func TestFreeLeases_DeletesOnlyTheFailedDownloadsKeys(t *testing.T) {
	ctx := context.Background()
	kv := newTestBus(t).KV(events.BucketLeases)
	keys := packLeaseKeys("the-wire-s01e01", "the-wire-s01e02", "the-wire-s01e03")

	_, err := kv.Create(ctx, keys[0], []byte("failed-download"))
	require.NoError(t, err)
	_, err = kv.Create(ctx, keys[1], []byte("later-download"))
	require.NoError(t, err)
	// keys[2]: never taken.

	pack := commonv1.MediaRef{
		Kind: commonv1.MediaKindSeries, Name: "the-wire",
		Keys: []string{"the-wire-s01e01", "the-wire-s01e02", "the-wire-s01e03"},
	}
	freed, err := FreeLeases(ctx, kv, "media", pack, "failed-download")
	require.NoError(t, err)
	assert.Equal(t, []string{keys[0]}, freed)

	_, err = kv.Get(ctx, keys[0])
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the failed Download's lease is freed")
	entry, err := kv.Get(ctx, keys[1])
	require.NoError(t, err)
	assert.Equal(t, "later-download", string(entry.Value), "a lease another grab holds is not the failed one's to free")

	freed, err = FreeLeases(ctx, kv, "media", pack, "failed-download")
	require.NoError(t, err)
	assert.Empty(t, freed, "a second call frees nothing")

	_, err = FreeLeases(ctx, kv, "media", commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "radiohead"}, "x")
	assert.ErrorIs(t, err, ErrUnsupportedKind)
}

// reclaimAfterReadKV is a real second writer interleaved into FreeLeases: the
// first Get of a key returns what was stored, and then a grab reclaims that
// key -- acquireLease's revision-checked Update -- before the caller acts on
// the stale read.
type reclaimAfterReadKV struct {
	events.KV
	t         *testing.T
	reclaimer string
	done      bool
}

func (k *reclaimAfterReadKV) Get(ctx context.Context, key string) (events.Entry, error) {
	entry, err := k.KV.Get(ctx, key)
	if err == nil && !k.done {
		k.done = true
		_, uerr := k.Update(ctx, key, []byte(k.reclaimer), entry.Revision)
		require.NoError(k.t, uerr, "the interleaved grab reclaims the lease")
	}
	return entry, err
}

// TestFreeLeases_KeepsALeaseReclaimedAfterTheRead pins the race a value check
// followed by a plain Delete loses: a grab that reclaims the failed
// Download's lease between FreeLeases' read and its delete must keep it.
func TestFreeLeases_KeepsALeaseReclaimedAfterTheRead(t *testing.T) {
	ctx := context.Background()
	inner := newTestBus(t).KV(events.BucketLeases)
	key := packLeaseKeys("the-wire-s01e01")[0]
	_, err := inner.Create(ctx, key, []byte("failed-download"))
	require.NoError(t, err)

	kv := &reclaimAfterReadKV{KV: inner, t: t, reclaimer: "new-grab"}
	episode := commonv1.MediaRef{
		Kind: commonv1.MediaKindSeries, Name: "the-wire", Keys: []string{"the-wire-s01e01"},
	}
	freed, err := FreeLeases(ctx, kv, "media", episode, "failed-download")
	require.NoError(t, err)
	assert.Empty(t, freed, "a lease reclaimed after the read is not the failed Download's to free")

	entry, err := inner.Get(ctx, key)
	require.NoError(t, err, "the reclaiming grab's lease must survive")
	assert.Equal(t, "new-grab", string(entry.Value))
}
