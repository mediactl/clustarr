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

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestPreferLargestAgainstRealSizeTables is a regression test for a review
// finding on Task C2 fix round 1: an absolute-tolerance check
// (PrefMBPerMin >= MaxMBPerMin-1) matches the movie/anime "biggest" sentinel
// (1999/2000) but misses the series one (995/1000, a difference of 5), so
// preferLargest silently took the wrong branch for every standard TV series
// size table entry. Exercised against the real quality.*SizeTable() data,
// not synthetic values, because that mismatch is exactly what hid the bug
// the first time -- every hand-written RankKey-based rank test bypasses
// preferLargest entirely.
func TestPreferLargestAgainstRealSizeTables(t *testing.T) {
	t.Run("movie table's top tier is the biggest sentinel (1999/2000)", func(t *testing.T) {
		sl := quality.MovieSizeTable()["Bluray-1080p"]
		require.True(t, preferLargest(sl))
	})
	t.Run("series table's top tier is the biggest sentinel (995/1000)", func(t *testing.T) {
		sl := quality.SeriesSizeTable()["Bluray-1080p"]
		require.True(t, preferLargest(sl))
	})
	t.Run("anime table's top tier is the biggest sentinel (1999/2000)", func(t *testing.T) {
		sl := quality.AnimeSizeTable()["Bluray-1080p"]
		require.True(t, preferLargest(sl))
	})
	t.Run("an ordinary profile override with a real preferred size is not the sentinel", func(t *testing.T) {
		sl := quality.SizeLimit{MinMBPerMin: 5, PrefMBPerMin: 15, MaxMBPerMin: 25}
		require.False(t, preferLargest(sl))
	})
}

// TestBuildRankKeyThroughSeriesSizeTable proves buildRankKey itself (not
// just preferLargest in isolation) takes the prefer-largest branch for a
// real series Target/profile -- the existing Rank tests all construct
// RankKey values directly and never call buildRankKey, so they could not
// have caught the series-table regression above.
func TestBuildRankKeyThroughSeriesSizeTable(t *testing.T) {
	bluray1080, ok := quality.Lookup("video", "Bluray-1080p")
	require.True(t, ok)

	p := quality.Profile{Sizes: quality.SeriesSizeTable(), ProperPolicy: "preferAndUpgrade"}
	tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42}}
	parsed := &release.ParsedRelease{Episodes: []int{5}}
	rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 3_000_000_000}

	key := buildRankKey(p, Options{}, tg, parsed, rel, 0)

	require.True(t, key.PreferLargestSize, "a series size table entry is TRaSH's biggest sentinel, not a literal 995 MB/min target")
	require.Equal(t, rel.SizeBytes, key.SizeBytes)
	require.Zero(t, key.SizeDeltaBucket, "SizeDeltaBucket is unused once PreferLargestSize is true")
}
