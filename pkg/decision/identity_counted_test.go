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
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/release"
)

// onePieceNumbering is One Piece as the library holds it (talos,
// 2026-11-28): TVDB's 23 seasons by their sizes, the absolute number
// counting through them, and the specials -- TVDB gives a crossover its
// absolute number (590, which season 16 therefore skips) and the rest 0.
func onePieceNumbering() []decision.EpisodeNumbering {
	sizes := []int{8, 22, 17, 13, 9, 22, 39, 13, 52, 31, 99, 56, 100, 35, 62, 49, 118, 33, 98, 14, 194, 70, 25}
	out := []decision.EpisodeNumbering{
		{Season: 0, Episode: 1, Absolute: 0},
		{Season: 0, Episode: 2, Absolute: 0},
		{Season: 0, Episode: 39, Absolute: 590},
	}
	absolute := 0
	for i, n := range sizes {
		for e := 1; e <= n; e++ {
			absolute++
			if absolute == 590 {
				absolute++
			}
			out = append(out, decision.EpisodeNumbering{Season: i + 1, Episode: e, Absolute: absolute})
		}
	}
	return out
}

const onePieceS23E02 = "One.Piece.S01E1157.Nami.in.a.Fix.An.Adventure.in.Block.Kingdom.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG"

// TestASeasonOneNumberCountsThroughTheSeries: a streaming service lists a
// long-running anime as one season, and the release of its 1157th episode
// is named S01E1157. Season 1 has eight episodes, so the number was read
// as an episode the series does not have and the release refused for
// S23E02, the episode it is: "WrongItem: release is S01E1157, the item is
// S23E02, absolute [1157], aired 2026-04-12" (talos, 2026-11-28).
func TestASeasonOneNumberCountsThroughTheSeries(t *testing.T) {
	numbering := onePieceNumbering()
	require.Equal(t, decision.EpisodeNumbering{Season: 23, Episode: 25, Absolute: 1180}, numbering[len(numbering)-1],
		"the fixture counts as the library does")
	episode := func(season, number, absolute int, numbering []decision.EpisodeNumbering) decision.Target {
		return decision.Target{Kind: common.MediaKindEpisode, Available: true, Anime: true, Identity: decision.Identity{
			Titles: []string{"One Piece"}, Season: season, Episodes: []int{number}, Absolute: []int{absolute},
			Numbering: numbering, SingleEpisodeSearch: true,
		}}
	}
	// A series whose absolute count does not start at its first episode:
	// season 1's fifth episode is the sixth of the count.
	offset := []decision.EpisodeNumbering{
		{Season: 1, Episode: 1, Absolute: 2},
		{Season: 1, Episode: 2, Absolute: 3},
		{Season: 1, Episode: 3, Absolute: 4},
		{Season: 1, Episode: 4, Absolute: 5},
		{Season: 1, Episode: 5, Absolute: 6},
		{Season: 2, Episode: 1, Absolute: 7},
	}
	// Two regular episodes under one absolute number.
	shared := []decision.EpisodeNumbering{
		{Season: 1, Episode: 1, Absolute: 1}, {Season: 2, Episode: 1, Absolute: 9}, {Season: 3, Episode: 1, Absolute: 9},
	}

	for _, tc := range []struct {
		name   string
		target decision.Target
		title  string
		want   string
		detail string
	}{
		{
			name: "the defect: without the series' numbering the number is season 1's", target: episode(23, 2, 1157, nil), title: onePieceS23E02,
			want: "WrongItem", detail: "release is S01E1157, the item is S23E02, absolute [1157]",
		},
		{name: "with it, the release is the episode holding the number", target: episode(23, 2, 1157, numbering), title: onePieceS23E02},
		{
			name: "and not its neighbour", target: episode(23, 3, 1158, numbering), title: onePieceS23E02,
			want: "WrongItem", detail: "release is S01E1157 (counted through the series; S23E02), the item is S23E03",
		},
		{name: "a number season 1 has is season 1's", target: episode(1, 5, 5, numbering), title: "One.Piece.S01E05.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG"},
		{name: "a number past season 1 is the count's from the start", target: episode(2, 7, 15, numbering), title: "One.Piece.S01E15.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG"},
		{
			name: "another season's number is read as written", target: episode(23, 2, 1157, numbering), title: "One.Piece.S02E1157.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
			want: "WrongItem", detail: "release is S02E1157, the item is S23E02",
		},
		{
			name: "a number no episode holds is read as written", target: episode(23, 2, 1157, numbering), title: "One.Piece.S01E1300.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
			want: "WrongItem", detail: "release is S01E1300, the item is S23E02",
		},
		{
			name: "a special's absolute number is not in the count", target: episode(16, 12, 591, numbering), title: "One.Piece.S01E590.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
			want: "WrongItem", detail: "release is S01E590, the item is S16E12",
		},
		{
			name: "where the count is offset, the name still says season 1", target: episode(1, 4, 5, offset), title: "One.Piece.S01E05.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
			want: "WrongItem", detail: "release is S01E05, the item is S01E04",
		},
		{name: "and a number past it is the count's", target: episode(2, 1, 7, offset), title: "One.Piece.S01E07.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG"},
		{
			name: "a number two episodes hold names neither", target: episode(2, 1, 9, shared), title: "One.Piece.S01E09.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
			want: "WrongItem", detail: "release is S01E09, the item is S02E01",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertIdentity(t, evaluateOne(t, tc.target, tc.title, textIndexer, nil), tc.want, tc.detail)
		})
	}

	t.Run("a season search takes it for the episode it is", func(t *testing.T) {
		id := decision.Identity{Titles: []string{"One Piece"}, Season: 23, Numbering: numbering, SeasonSearch: true}
		for e := 1; e <= 25; e++ {
			id.Episodes, id.Absolute = append(id.Episodes, e), append(id.Absolute, 1155+e)
		}
		d := evaluateOne(t, decision.Target{Kind: common.MediaKindEpisode, Available: true, Anime: true, Identity: id}, onePieceS23E02, textIndexer, nil)
		assertIdentity(t, d, "", "")
		require.NotNil(t, d.Coverage)
		require.Equal(t, []decision.SeasonEpisode{{Season: 23, Episode: 2}}, d.Coverage.Episodes,
			"the grab planner and the season's controller read the covered episode from here")
		require.Empty(t, d.Coverage.Absolute)
	})

	t.Run("the importer reads the same place", func(t *testing.T) {
		p, err := release.Parse(onePieceS23E02, release.Options{Kind: common.MediaKindEpisode})
		require.NoError(t, err)
		require.Equal(t, []int{1}, p.Seasons)
		require.Equal(t, []int{1157}, p.Episodes)
		require.Empty(t, p.Absolute, "the name carries no bare absolute number: only the pair says 1157")
		cov := decision.CoverageFor(p, decision.Identity{Numbering: numbering})
		require.Equal(t, []decision.SeasonEpisode{{Season: 23, Episode: 2}}, cov.Episodes)
		require.Equal(t, []decision.SeasonEpisode{{Season: 1, Episode: 1157}}, decision.CoverageFor(p, decision.Identity{}).Episodes)
	})
}
