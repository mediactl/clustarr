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

func TestTargetRuntimeMinutes(t *testing.T) {
	t.Run("movie with known runtime", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 95}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 95, m)
	})

	t.Run("movie with unknown runtime falls back to 110", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 0}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 110, m)
	})

	t.Run("single episode with known runtime", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 42, m)
	})

	t.Run("single episode with unknown runtime falls back to 45", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{0}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("season pack sums per-episode runtimes, falling back per episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42, 42, 0, 42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{1, 2, 3, 4}})
		require.True(t, ok)
		require.Equal(t, 171, m) // 42+42+45+42, the third episode's unknown runtime falls back to 45
	})

	t.Run("full-season release with no per-episode runtime data falls back to one 45-minute episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{FullSeason: true})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("non-video kind has no runtime/size model", func(t *testing.T) {
		_, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindBook}, &release.ParsedRelease{})
		require.False(t, ok)
	})
}

func TestSizeRejections(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	tg := Target{Kind: common.MediaKindMovie} // RuntimeMinutes 0 -> falls back to 110

	t.Run("within bounds", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 10_200_000_000} // ~9.5 GiB
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})

	t.Run("below the 50.8 MB/min floor at 110 minutes", func(t *testing.T) {
		// 50.8 MB/min * 110 min * 1024 * 1024 = 5,859,442,688 bytes exactly.
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 5_000_000_000}
		got := sizeRejections(tg, p, &release.ParsedRelease{}, rel)
		require.Len(t, got, 1)
		require.Equal(t, common.RejectionPermanent, got[0].Type)
		require.Contains(t, got[0].Reason, ReasonBelowMinimumSize.Code)
	})

	t.Run("zero size is unknown, not rejected -- ported from AcceptableSizeSpecification", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 0}
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})

	t.Run("quality absent from the size table is not checked", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: common.Quality{Name: "Unknown-Table-Entry"}, SizeBytes: 1}
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})
}

// TestASeasonSearchSizesBySumOfCoveredRuntimes is series search design §6.1
// step 3: a season search measures a release against the runtimes of the
// episodes of the season it covers (RuntimeByEpisode, 45 minutes per
// unknown episode), so a single is not measured against the whole season
// nor a pack against one episode. Releases go through the real parser.
func TestASeasonSearchSizesBySumOfCoveredRuntimes(t *testing.T) {
	tg := Target{Kind: common.MediaKindSeries, Identity: Identity{
		Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{1, 2, 3, 4}, SeasonSearch: true,
	}, RuntimeByEpisode: map[int]int{1: 58, 2: 48, 3: 0, 4: 47}}
	minutes := func(tg Target, title string) int {
		t.Helper()
		p, err := release.Parse(title, release.Options{Kind: common.MediaKindEpisode})
		require.NoError(t, err)
		m, ok := targetRuntimeMinutes(tg, p)
		require.True(t, ok)
		return m
	}

	require.Equal(t, 48, minutes(tg, "Breaking.Bad.S01E02.720p.HDTV.x264-GRP"), "a single is its own episode's runtime")
	require.Equal(t, 45, minutes(tg, "Breaking.Bad.S01E03.720p.HDTV.x264-GRP"), "an unknown runtime falls back to 45 minutes")
	require.Equal(t, 48+45, minutes(tg, "Breaking.Bad.S01E02E03.720p.HDTV.x264-GRP"), "a multi-episode release sums the episodes it covers")
	require.Equal(t, 58+48+45+47, minutes(tg, "Breaking.Bad.S01.720p.BluRay.x264-GRP"), "a full-season pack sums every episode of the season")
	require.Equal(t, 2*(58+48+45+47), minutes(tg, "Breaking.Bad.S01-S02.720p.BluRay.x264-GRP"), "a multi-season pack is measured as that many seasons")
	require.Equal(t, 45, minutes(tg, "Breaking.Bad.S02E01.720p.HDTV.x264-GRP"), "covering nothing of the season is one fallback episode")

	// Every other path is unchanged: without SeasonSearch the per-release
	// EpisodeRuntimes rule runs, and RuntimeByEpisode is not read.
	other := tg
	other.Identity.SeasonSearch = false
	other.EpisodeRuntimes = []int{30}
	require.Equal(t, 30, minutes(other, "Breaking.Bad.S01E02.720p.HDTV.x264-GRP"))
}
