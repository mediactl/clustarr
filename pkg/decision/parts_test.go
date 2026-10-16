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
package decision_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

const gb = int64(1) << 30

// post is one release as an indexer listed it.
type post struct {
	indexer, title string
	size           int64
	days           int // published this many days before 2026-10-16
}

func posts(ps ...post) []common.ReleaseInfo {
	out := make([]common.ReleaseInfo, 0, len(ps))
	for i, p := range ps {
		at := metav1.NewTime(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -p.days))
		out = append(out, common.ReleaseInfo{
			GUID: p.indexer + ":" + string(rune('a'+i)), IndexerRef: p.indexer, IndexerName: p.indexer,
			Protocol: common.ProtocolUsenet, Title: p.title, SizeBytes: p.size, PublishedAt: &at,
		})
	}
	return out
}

func filmTarget(title string, year, runtime int) decision.Target {
	return decision.Target{
		Kind: common.MediaKindMovie, Monitored: true, Available: true, OriginalLanguageTag: "en",
		Identity: decision.Identity{Titles: []string{title}, Year: year}, RuntimeMinutes: runtime,
	}
}

// sets runs the search worker's composition: the releases judged, the part
// sets among them each judged as one release (decision.PartSets).
func sets(t *testing.T, tg decision.Target, rels []common.ReleaseInfo) []decision.Decision {
	t.Helper()
	p, cat := builtin(t, "hd-bluray-web")
	ctx := context.Background()
	ds := decision.EvaluateRelease(ctx, tg, p, cat, rels, usenetOnly)
	var out []decision.Decision
	for _, s := range decision.PartSets(ds) {
		judged := decision.EvaluateRelease(ctx, tg, p, cat, []common.ReleaseInfo{s.Release()}, usenetOnly)
		require.Len(t, judged, 1)
		out = append(out, s.Judge(judged[0]))
	}
	return out
}

// 56 Up exists in 1080p only as three posted parts, on both indexers
// (2026-10-16): each indexer's three are one set, approved as the film the
// parts are not; the 720p parts are a set of their own, missing Part 3 of
// the 720p ones is no set, and the whole DVD rip is no set.
func TestAMoviePostedInPartsIsOneSetPerIndexer(t *testing.T) {
	rels := posts(
		post{"nzbgeek", "56.Up.2012.Part3.1080p.BluRay.x264-USURY", 38 * gb / 10, 1137},
		post{"nzbgeek", "56.Up.2012.Part1.1080p.BluRay.x264-USURY", 38 * gb / 10, 1137},
		post{"nzbgeek", "56.Up.2012.Part2.1080p.BluRay.x264-USURY", 38 * gb / 10, 1137},
		post{"nzb-life", "56.Up.2012.Part1.1080p.BluRay.x264-USURY-xpost", 36 * gb / 10, 2501},
		post{"nzb-life", "56.Up.2012.Part2.1080p.BluRay.x264-USURY-xpost", 36 * gb / 10, 2501},
		post{"nzb-life", "56.Up.2012.Part3.1080p.BluRay.x264-USURY-xpost", 36 * gb / 10, 2501},
		post{"nzb-life", "56.Up.2012.Part1.720p.BluRay.x264-USURY-xpost", 24 * gb / 10, 2501},
		post{"nzb-life", "56.Up.2012.Part2.720p.BluRay.x264-USURY-xpost", 24 * gb / 10, 2501},
		post{"nzb-life", "56.Up.2012.DVDRip.XviD-WiDE", 17 * gb / 10, 4853},
	)
	got := sets(t, filmTarget("56 Up", 2012, 144), rels)
	require.Len(t, got, 3, "nzbgeek's 1080p, nzb-life's 1080p, nzb-life's 720p")
	byIndexer := map[string]decision.Decision{}
	for _, d := range got {
		if d.Release.Quality.Resolution == 1080 {
			byIndexer[d.Release.IndexerRef] = d
		}
	}
	geek := byIndexer["nzbgeek"]
	assert.Equal(t, "56.Up.2012.1080p.BluRay.x264-USURY", geek.Release.Title)
	assert.Equal(t, 3*(38*gb/10), geek.Release.SizeBytes)
	assert.True(t, geek.Approved, "the film the parts make is approved: %v", reasons(geek))
	require.Len(t, geek.Release.Parts, 3)
	for i, part := range geek.Release.Parts {
		assert.Equal(t, int32(i+1), part.Part)
	}
	assert.Equal(t, "56.Up.2012.Part2.1080p.BluRay.x264-USURY", geek.Release.Parts[1].Title)
	assert.Contains(t, geek.Release.GUID, "parts:")
	assert.Len(t, byIndexer, 2)

	again := sets(t, filmTarget("56 Up", 2012, 144), rels)
	assert.Equal(t, got[0].Release.GUID, again[0].Release.GUID, "a set's GUID is stable across searches")
}

// No Direction Home's Blu-ray is two posted parts (2026-10-16): a part
// posted twice is one part of the set, the newer post taken; group case
// and an obfuscation suffix do not split a set.
func TestASetTakesOnePostOfEachPart(t *testing.T) {
	rels := posts(
		post{"nzb-life", "No.Direction.Home.Bob.Dylan.2005.Part.1.1080p.BluRay.x264-RedBlade", 121 * gb / 10, 3632},
		post{"nzb-life", "No.Direction.Home.Bob.Dylan.2005.Part.1.1080p.BluRay.x264-RedBlade", 121 * gb / 10, 3621},
		post{"nzb-life", "No.Direction.Home.Bob.Dylan.2005.Part.2.1080p.BluRay.x264-RedBlade-xpost", 119 * gb / 10, 2238},
		post{"nzbgeek", "No.Direction.Home.Bob.Dylan.2005.Part.1.1080p.BluRay.x264-REDBLADE", 121 * gb / 10, 3632},
		post{"nzbgeek", "No.Direction.Home.Bob.Dylan.2005.Part.2.1080p.BluRay.x264-RedBlade-Obfuscated", 111 * gb / 10, 3626},
	)
	got := sets(t, filmTarget("No Direction Home: Bob Dylan", 2005, 208), rels)
	require.Len(t, got, 2)
	for _, d := range got {
		require.Len(t, d.Release.Parts, 2, d.Release.IndexerRef)
		assert.True(t, d.Approved, "%s: %v", d.Release.IndexerRef, reasons(d))
		if d.Release.IndexerRef == "nzb-life" {
			assert.Equal(t, "nzb-life:b", d.Release.Parts[0].GUID, "the newer post of part 1")
		}
	}
}

// What only looks like a set is none: a part token inside the title (two
// films, "Che: Part One" and "Part Two"), parts that skip, one lone part,
// and an episode search.
func TestWhatOnlyLooksLikeASetIsNone(t *testing.T) {
	for name, tc := range map[string]struct {
		tg   decision.Target
		rels []common.ReleaseInfo
	}{
		// Che's two films share a year; a target whose titles take both
		// releases leaves only the title rule to tell them apart.
		"the part is the title's": {decision.Target{
			Kind: common.MediaKindMovie, Monitored: true, Available: true, OriginalLanguageTag: "en", RuntimeMinutes: 135,
			Identity: decision.Identity{Titles: []string{"Che Part 1", "Che Part 2"}, Year: 2008},
		}, posts(
			post{"idx", "Che.Part.1.2008.1080p.BluRay.x264-GRP", 9 * gb, 10},
			post{"idx", "Che.Part.2.2008.1080p.BluRay.x264-GRP", 9 * gb, 10})},
		"parts 1 and 3": {filmTarget("56 Up", 2012, 144), posts(
			post{"idx", "56.Up.2012.Part1.1080p.BluRay.x264-USURY", 4 * gb, 10},
			post{"idx", "56.Up.2012.Part3.1080p.BluRay.x264-USURY", 4 * gb, 10})},
		"one part": {filmTarget("56 Up", 2012, 144), posts(
			post{"idx", "56.Up.2012.Part1.1080p.BluRay.x264-USURY", 4 * gb, 10})},
		// 28 Up's USURY Blu-ray is posted both as its two parts and as one
		// post holding both (2026-10-16): the one download is taken.
		"the encode posted whole too": {filmTarget("28 Up", 1984, 136), posts(
			post{"nzb-life", "28.Up.1984.1080p.BluRay.x264-USURY-xpost", 108 * gb / 10, 2501},
			post{"nzb-life", "28.Up.1984.Part1.1080p.BluRay.x264-USURY-xpost", 49 * gb / 10, 2501},
			post{"nzb-life", "28.Up.1984.Part2.1080p.BluRay.x264-USURY-xpost", 61 * gb / 10, 2501})},
		"other groups": {filmTarget("56 Up", 2012, 144), posts(
			post{"idx", "56.Up.2012.Part1.1080p.BluRay.x264-USURY", 4 * gb, 10},
			post{"idx", "56.Up.2012.Part2.1080p.BluRay.x264-OTHER", 4 * gb, 10})},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, sets(t, tc.tg, tc.rels))
		})
	}
}
