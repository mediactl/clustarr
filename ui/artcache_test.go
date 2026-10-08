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

package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestArtCacheEvictsByBytes: /art's byte cache (artwork design §B.8 as
// amended 2026-10-07) is bounded by bytes, not entries: an add beyond the
// budget evicts the least recently used, a get refreshes recency, an entry
// over a quarter of the budget is refused, and a zero budget keeps nothing.
func TestArtCacheEvictsByBytes(t *testing.T) {
	body := func(n int) []byte { return []byte(strings.Repeat("x", n)) }
	has := func(c *artCache, digest string) bool { _, ok := c.get(digest); return ok }

	c := newArtCache(100)
	c.add("a", body(25))
	c.add("b", body(25))
	c.add("c", body(25))
	c.add("d", body(25))
	require.True(t, has(c, "a"), "100 bytes fit a 100-byte budget") // a is now the most recent
	require.Equal(t, int64(100), c.used)

	c.add("e", body(25))
	require.False(t, has(c, "b"), "the least recently used goes first; a's get kept it")
	require.True(t, has(c, "a"))
	require.True(t, has(c, "e"))
	require.Equal(t, int64(100), c.used)

	c.add("f", body(20))
	c.add("g", body(20))
	require.False(t, has(c, "c"), "two adds evict as many entries as their bytes need")
	require.False(t, has(c, "d"))
	require.LessOrEqual(t, c.used, int64(100))

	c.add("big", body(26))
	require.False(t, has(c, "big"), "an entry over a quarter of the budget is served, never kept")
	require.True(t, has(c, "a"), "a refused entry evicts nothing")

	c.add("a", body(25))
	require.LessOrEqual(t, c.used, int64(100), "a digest already kept is not counted twice")

	zero := newArtCache(0)
	zero.add("a", body(1))
	require.False(t, has(zero, "a"), "a zero budget keeps nothing")

	var none *artCache
	none.add("a", body(1))
	require.False(t, has(none, "a"), "a nil cache is a no-op")
}
