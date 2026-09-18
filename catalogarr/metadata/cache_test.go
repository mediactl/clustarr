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

package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

type cacheFixture struct {
	Title string `json:"title"`
}

// newTestKV wires an in-process membus with the real default topology
// (ForSingleNode, matching rpc_test.go's newTestBus): Topology.Validate
// requires the CLUSTARR_DLQ stream on every topology, including one that
// only cares about a bucket, so hand-rolling a Buckets-only topology (as an
// earlier draft of this test did) fails Ensure. events.Default() already
// declares BucketMetadataCache with a 30d TTL (§5), so this is also the
// production shape, not a test-only approximation of it.
func newTestKV(t *testing.T, clock clockwork.Clock) events.KV {
	t.Helper()
	bus := membus.New(clock)
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	return bus.KV(events.BucketMetadataCache)
}

func TestKVCacheMissThenHitThenExpiry(t *testing.T) {
	clock := clockwork.NewFakeClock()
	c := newKVCache(newTestKV(t, clock), clock)
	ctx := context.Background()

	var out cacheFixture
	hit, err := c.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.False(t, hit, "empty bucket must miss")

	require.NoError(t, c.Set(ctx, "movie:tmdb=27205", cacheFixture{Title: "Inception"}, time.Hour))

	hit, err = c.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "Inception", out.Title)

	clock.Advance(2 * time.Hour)
	hit, err = c.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.False(t, hit, "an entry past its own expiresAt must miss even though the bucket TTL (30d) has not elapsed")
}
