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
	"github.com/mediactl/clustarr/pkg/release"
)

// pantyAndStocking is TheXEM's table for TVDB 188551 where the cases need
// it (thexem.info/map/all?id=188551&origin=tvdb, 2026-11-26), each TVDB
// episode under its title, and TheXEM's scene title for its second season.
// TVDB lists every short episode; a broadcast holds two or three.
func pantyAndStocking() Identity {
	row := func(ss, se, ts, te int, title string) SceneMapping {
		return SceneMapping{Scene: EpisodeNumbering{Season: ss, Episode: se}, TVDB: EpisodeNumbering{Season: ts, Episode: te}, Title: title}
	}
	return Identity{
		SceneMappings: []SceneMapping{
			row(1, 7, 1, 13, "Once Upon a Time in Garterbelt"), row(1, 7, 1, 14, "1 Angry Ghost"),
			row(2, 4, 2, 7, "Daten City President"), row(2, 4, 2, 8, "Pet Cematary Hills"), row(2, 4, 2, 9, "Shoot for Yesterday!"),
			row(2, 7, 2, 14, "Independence Dick"), row(2, 7, 2, 15, "Longest Bitch Yard"),
			row(2, 10, 2, 22, "The Sex From Another World"), row(2, 10, 2, 23, "Lord of the Kokan the Great"),
		},
		SceneSeasonNames: []SceneSeasonName{{Title: "New Panty & Stocking with Garterbelt", Season: 2}},
	}
}

// A release's numbering on a series whose broadcasts hold several of
// TVDB's episodes, each name parsed as the search agent parses it. On talos
// (2026-11-26) the first was grabbed for S02E14 and S02E15, the fourth for
// the 2010 season's S01E14, and the third's ".14.15" was not read at all.
func TestAReleaseOfABroadcastOrOfOneOfItsEpisodesCoversWhatItHolds(t *testing.T) {
	se := func(pairs ...int) []SeasonEpisode {
		var out []SeasonEpisode
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, SeasonEpisode{Season: pairs[i], Episode: pairs[i+1]})
		}
		return out
	}
	for _, tc := range []struct {
		name, title string
		want        []SeasonEpisode
	}{
		{"TVDB's episode under its own title is that episode", "PANTY.and.STOCKING.with.GARTERBELT.S02E07.Daten.City.President.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-Kitsune.mkv-xpost", se(2, 7)},
		{"a title that is not that episode's leaves the scene reading", "PANTY.and.STOCKING.with.GARTERBELT.S02E07.Independence.Dick.1080p.AMZN.WEB-DL.H.264-GRP", se(2, 14, 2, 15)},
		{"a bare number is the broadcast", "PANTY.and.STOCKING.With.GARTERBELT.S02E07.1080p.AMZN.WEB-DL.DDP5.1.H.264-ppkhoa", se(2, 14, 2, 15)},
		{"a broadcast with the episodes it holds", "Panty.and.Stocking.with.Garterbelt.S02E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-VARYG", se(2, 14, 2, 15)},
		{"a list the table does not hold is read as written", "Panty.and.Stocking.with.Garterbelt.S02E07.20.21.1080p.AMZN.WEB-DL.H.264-GRP", se(2, 7, 2, 20, 2, 21)},
		{"the season's own title numbers it season 1", "New.PANTY.and.STOCKING.with.GARTERBELT.S01E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.265.MSubs-ToonsHub", se(2, 14, 2, 15)},
		{"and without a list", "[ToonsHub] New PANTY and STOCKING with GARTERBELT S01E10 1080p AMZN WEB-DL DDP5.1 H.265 (Dual-Audio, Multi-Subs)", se(2, 22, 2, 23)},
		{"the series' title keeps season 1", "PANTY.and.STOCKING.with.GARTERBELT.S01E07.1080p.BluRay.x264-GRP", se(1, 13, 1, 14)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			require.Equal(t, tc.want, CoverageFor(p, pantyAndStocking()).Episodes)
		})
	}
}
