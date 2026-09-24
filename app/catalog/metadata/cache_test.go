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
	"regexp"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
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

// newTestLRU builds a small in-process L1 cache for tiered-cache tests.
func newTestLRU(t *testing.T, clock clockwork.Clock) (*pkgmetadata.LRUCache, error) {
	t.Helper()
	return pkgmetadata.NewLRUCache(64, clock)
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

func TestCacheKeyIsDeterministicAndSortedByIDKey(t *testing.T) {
	ids := pkgmetadata.ExternalIDs{pkgmetadata.KeyIMDb: "tt1375666", pkgmetadata.KeyTMDB: "27205"}
	require.Equal(t, "movie.imdb=tt1375666_tmdb=27205", cacheKey(commonv1.MediaKindMovie, ids))
	// Order of the input map must not affect the key.
	ids2 := pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205", pkgmetadata.KeyIMDb: "tt1375666"}
	require.Equal(t, cacheKey(commonv1.MediaKindMovie, ids), cacheKey(commonv1.MediaKindMovie, ids2))
}

// Every key this builds is written to a NATS KV bucket, whose keys must match
// ^[-/_=\.a-zA-Z0-9]+$. The original separators (":" and ",") were both
// illegal, so every L2 read and write failed with "nats: invalid key" and
// every metadata refresh redelivered forever without status.metadata ever
// landing. Nothing caught it before the first run on a real cluster: the unit
// and envtest suites use the in-memory bus, which enforces no key grammar.
func TestCacheKeyIsALegalNATSKVKey(t *testing.T) {
	legal := regexp.MustCompile(`^[-/_=.a-zA-Z0-9]+$`)

	for _, tc := range []struct {
		name string
		kind commonv1.MediaKind
		ids  pkgmetadata.ExternalIDs
	}{
		{"movie with two ids", commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "27205", "imdb": "tt1375666"}},
		{"series by tvdb", commonv1.MediaKindSeries, pkgmetadata.ExternalIDs{"tvdb": "121361"}},
		{"no ids at all", commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{}},
		// Provider ids come from outside this process and are not guaranteed
		// to be alphanumeric; musicbrainz ids are the realistic case, and a
		// hostile or malformed one must not be able to make the key illegal.
		{"musicbrainz uuid", commonv1.MediaKindArtist, pkgmetadata.ExternalIDs{"musicbrainz": "f4a31f0a-51dd-4fa7-986d-3095c40c5ed9"}},
		{"value carrying separators", commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"weird": "a:b,c d/e.f"}},
		{"key carrying separators", commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"a:b,c": "1"}},
		{"unicode", commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "Wall·E"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheKey(tc.kind, tc.ids)
			assert.Regexp(t, legal, got, "cacheKey produced a key NATS KV will reject: %q", got)
		})
	}
}

// Sanitising must not collapse two different ids onto one key -- that would
// silently serve one item's metadata for another.
func TestCacheKeySanitisingDoesNotCollide(t *testing.T) {
	a := cacheKey(commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "a:b"})
	b := cacheKey(commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "a,b"})
	assert.NotEqual(t, a, b, "two distinct ids must not sanitise to one key")
}
