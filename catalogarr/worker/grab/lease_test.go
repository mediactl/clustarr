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

	"github.com/mediactl/clustarr/pkg/events"
)

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

	acquired, err := acquireLeases(ctx, kv, keys, "download-a")
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
			_, err := acquireLeases(ctx, kv, keys, downloadName)
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

	acquired, err := acquireLeases(ctx, kv, keys, "download-a")
	require.NoError(t, err)
	releaseLeases(ctx, kv, acquired)
	releaseLeases(ctx, kv, acquired) // deleting an absent key is not an error

	_, err = kv.Get(ctx, keys[0])
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}
