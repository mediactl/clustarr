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
	"context"
	"fmt"
	"testing"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
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

// TestPreferLargestRatioAgainstEveryShippedEntry is ruling R-12's evidence:
// every entry of all three shipped size tables -- not only each table's top
// tier, which is all TestPreferLargestAgainstRealSizeTables checks -- reads
// as the "biggest" sentinel, with margin to spare above preferLargestRatio.
// An edited table entry that drifts toward the threshold fails here by name.
func TestPreferLargestRatioAgainstEveryShippedEntry(t *testing.T) {
	const lowestShipped = 0.995 // Sonarr's 995/1000
	for name, table := range map[string]map[string]quality.SizeLimit{
		"movie":  quality.MovieSizeTable(),
		"series": quality.SeriesSizeTable(),
		"anime":  quality.AnimeSizeTable(),
	} {
		require.NotEmpty(t, table, name)
		for q, sl := range table {
			require.True(t, preferLargest(sl), "%s table %s (%v/%v) must read as the biggest sentinel", name, q, sl.PrefMBPerMin, sl.MaxMBPerMin)
			require.GreaterOrEqual(t, sl.PrefMBPerMin/sl.MaxMBPerMin, lowestShipped,
				"%s table %s sits closer to preferLargestRatio than any upstream table does; revisit the threshold", name, q)
		}
	}
	require.Less(t, preferLargestRatio, lowestShipped, "the threshold must sit below every shipped sentinel")
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

// Revision.Version has no omitempty, so its CRD default of 1 never reaches a
// Revision written from Go; an unparsed one arrives as 0 and must rank as the
// original, not below it.
func TestCompareRevisionFloorsVersionAtOne(t *testing.T) {
	require.Zero(t, compareRevision(common.Revision{}, common.Revision{Version: 1}))
	require.Equal(t, 1, compareRevision(common.Revision{Version: 2}, common.Revision{}))
	require.Equal(t, -1, compareRevision(common.Revision{}, common.Revision{Version: 2}))
}

// Decision.Release lands in Search.status.results and Download.spec.release,
// where ReleaseInfo.MatchedFormats carries MaxItems=200: one release over it
// would get the whole apply rejected. This pins the helper evaluateOne
// applies to the Release's copy; Decision.Matched keeps every match.
func TestCapMatchedFormatsTruncatesToTheMaxItems(t *testing.T) {
	many := make([]string, maxMatchedFormats+5)
	for i := range many {
		many[i] = "f"
	}
	require.Len(t, capMatchedFormats(many), maxMatchedFormats)
	require.Equal(t, []string{"a", "b"}, capMatchedFormats([]string{"a", "b"}))
	require.Nil(t, capMatchedFormats(nil))
}

// TestEvaluateCapsMatchedFormatsOnTheRelease pins the call, not the helper:
// TestCapMatchedFormatsTruncatesToTheMaxItems passes with evaluateOne
// assigning the raw match list, so this drives the public Evaluate path with
// a catalogue whose every format matches any title and asserts the Release
// that reaches an apply carries at most the cap, while Decision.Matched keeps
// every match.
func TestEvaluateCapsMatchedFormatsOnTheRelease(t *testing.T) {
	const formats = maxMatchedFormats + 50
	anyTitle := regexp2.MustCompile(`.`, regexp2.IgnoreCase)
	cat := &catalogue.Catalogue{Formats: make(map[string]*catalogue.Format, formats)}
	for i := range formats {
		slug := fmt.Sprintf("any-title-%03d", i)
		cat.Formats[slug] = &catalogue.Format{
			Slug: slug, Name: slug, Scores: map[string]int{"default": 0},
			Conditions: []catalogue.Condition{{Kind: catalogue.CondReleaseTitle, Name: slug, Pattern: anyTitle}},
		}
	}
	bluray1080, ok := quality.Lookup("video", "Bluray-1080p")
	require.True(t, ok)
	p := quality.Profile{
		Tiers:        [][]quality.Definition{{bluray1080}},
		ProperPolicy: "preferAndUpgrade",
		LanguageName: "any",
		Sizes:        quality.MovieSizeTable(),
	}
	tg := Target{Kind: common.MediaKindMovie, Available: true, Identity: Identity{Titles: []string{"Arrival"}, Year: 2016}}
	rels := []common.ReleaseInfo{{Title: "Arrival.2016.1080p.BluRay.x264-GRP", GUID: "g", Protocol: common.ProtocolTorrent}}

	ds := Evaluate(context.Background(), tg, p, cat, rels, Options{UserInvoked: true})

	require.Len(t, ds, 1)
	require.Len(t, ds[0].Matched, formats, "every synthetic format matches any title")
	require.Len(t, ds[0].Release.MatchedFormats, maxMatchedFormats,
		"the Release is what lands in Search.status.results and Download.spec.release, where MaxItems=%d", maxMatchedFormats)
}
