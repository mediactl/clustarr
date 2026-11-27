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
			// Broadcast 12 of each season, without the titles: no case reads them.
			row(1, 12, 1, 23, ""), row(1, 12, 1, 24, ""),
			row(2, 12, 2, 27, ""), row(2, 12, 2, 28, ""),
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
		{"an anime group's name for the season, with its ampersand", "[DKB] New Panty & Stocking with Garterbelt-S01E12 [1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs][A6F199E7]", se(2, 27, 2, 28)},
		{"the series' title keeps season 1", "PANTY.and.STOCKING.with.GARTERBELT.S01E07.1080p.BluRay.x264-GRP", se(1, 13, 1, 14)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			require.Equal(t, tc.want, CoverageFor(p, pantyAndStocking()).Episodes)
		})
	}
}

// An absolute number on a series whose scene counts each season's
// broadcasts from 1, and whose broadcasts hold several of TVDB's episodes:
// TheXEM's rows for the eighth broadcast of each season of TVDB 188551
// (fetched 2026-11-26). On talos that day a search for S01E19 turned
// "Panty.And.Stocking.With.Garterbelt.E10.720p.BluRay.x264-GOTEi" away as
// the wrong item: absolute 10 had rows of two scene episodes, so it was read
// as TVDB's absolute 10, S01E10.
func TestAnAbsoluteNumberIsTheBroadcastItsTitlesSeasonCounts(t *testing.T) {
	row := func(ss, se, abs, ts, te, tabs int) SceneMapping {
		return SceneMapping{
			Scene: EpisodeNumbering{Season: ss, Episode: se, Absolute: abs},
			TVDB:  EpisodeNumbering{Season: ts, Episode: te, Absolute: tabs},
		}
	}
	rows := []SceneMapping{
		row(1, 8, 8, 1, 15, 15), row(1, 8, 8, 1, 16, 16),
		row(2, 8, 8, 2, 16, 42), row(2, 8, 8, 2, 17, 43), row(2, 8, 8, 2, 18, 44),
	}
	named := Identity{SceneMappings: rows, SceneSeasonNames: []SceneSeasonName{{Title: "New Panty & Stocking with Garterbelt", Season: 2}}}
	se := func(pairs ...int) []SeasonEpisode {
		var out []SeasonEpisode
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, SeasonEpisode{Season: pairs[i], Episode: pairs[i+1]})
		}
		return out
	}
	for _, tc := range []struct {
		name, title  string
		id           Identity
		wantEpisodes []SeasonEpisode
		wantAbsolute []int
	}{
		{"the series' title counts its first season", "Panty.And.Stocking.With.Garterbelt.E08.720p.BluRay.x264-GOTEi", named, se(1, 15, 1, 16), nil},
		{"an anime group's bare number likewise", "[Moozzi2] Panty & Stocking with Garterbelt-08 [BD 1920x1080 x265-10Bit FLACx3]", named, se(1, 15, 1, 16), nil},
		{"the later season's title counts that season", "[Group] New Panty & Stocking with Garterbelt - 08 [1080p]", named, se(2, 16, 2, 17, 2, 18), nil},
		{"no scene names: nothing says which, and the number is read as written", "Panty.And.Stocking.With.Garterbelt.E08.720p.BluRay.x264-GOTEi", Identity{SceneMappings: rows}, nil, []int{8}},
		{"one scene episode has the number: it is every episode of that broadcast", "Panty.And.Stocking.With.Garterbelt.E08.720p.BluRay.x264-GOTEi", Identity{SceneMappings: rows[:2]}, se(1, 15, 1, 16), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode, SeriesType: "anime"})
			require.NoError(t, err)
			require.Equal(t, []int{8}, p.Absolute, "the parser reads the bare number as absolute")
			c := CoverageFor(p, tc.id)
			require.Equal(t, tc.wantEpisodes, c.Episodes)
			require.Equal(t, tc.wantAbsolute, c.Absolute)
		})
	}
}
