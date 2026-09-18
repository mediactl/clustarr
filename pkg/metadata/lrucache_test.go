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

package metadata_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestLRUCacheExpiresByPerEntryTTL(t *testing.T) {
	clock := clockwork.NewFakeClock()
	c, err := metadata.NewLRUCache(8, clock)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "tmdb/movie/27205", metadata.Movie{Title: "Inception"}, time.Minute))

	var got metadata.Movie
	hit, err := c.Get(ctx, "tmdb/movie/27205", &got)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "Inception", got.Title)

	clock.Advance(2 * time.Minute)

	hit, err = c.Get(ctx, "tmdb/movie/27205", &got)
	require.NoError(t, err)
	require.False(t, hit, "entry must expire once its own TTL has passed")
}

func TestLRUCacheEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	clock := clockwork.NewFakeClock()
	c, err := metadata.NewLRUCache(1, clock)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "a", metadata.Movie{Title: "A"}, time.Hour))
	require.NoError(t, c.Set(ctx, "b", metadata.Movie{Title: "B"}, time.Hour))

	var got metadata.Movie
	hit, err := c.Get(ctx, "a", &got)
	require.NoError(t, err)
	require.False(t, hit, "capacity 1 must have evicted \"a\" when \"b\" was added")
}
