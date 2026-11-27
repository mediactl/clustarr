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

// The number after a title that ends in a season marker is that season's
// episode, each name parsed as the search and import agents parse it. On
// talos (2026-11-27) the first was filed at import as S01E01, and the second
// was grabbed as a pack of seasons 5 and 6.
func TestANumberUnderATitleThatNamesItsSeasonIsThatSeasonsEpisode(t *testing.T) {
	se := func(pairs ...int) []SeasonEpisode {
		var out []SeasonEpisode
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, SeasonEpisode{Season: pairs[i], Episode: pairs[i+1]})
		}
		return out
	}
	row := func(ss, se, sabs, ts, te, tabs int) SceneMapping {
		return SceneMapping{
			Scene: EpisodeNumbering{Season: ss, Episode: se, Absolute: sabs},
			TVDB:  EpisodeNumbering{Season: ts, Episode: te, Absolute: tabs},
		}
	}
	// A series TVDB keeps in one season and the scene splits in two.
	split := Identity{SceneMappings: []SceneMapping{row(1, 1, 1, 1, 1, 1), row(2, 1, 13, 1, 13, 13)}}
	// A series whose scene name for a season is not the marker's number.
	named := Identity{
		SceneMappings:    []SceneMapping{row(4, 5, 0, 4, 5, 0), row(3, 5, 0, 3, 5, 0)},
		SceneSeasonNames: []SceneSeasonName{{Title: "Shiguang Dailiren Season 3", Season: 4}},
	}
	// A group that counts on through the series under the season's name.
	counted := Identity{SceneMappings: []SceneMapping{row(3, 12, 49, 3, 12, 49)}}

	for _, tc := range []struct {
		name, title string
		id          Identity
		want        []SeasonEpisode
		absolute    []int
	}{
		{"with no table, the season's episode", "[SubsPlease] Pop Team Epic S2 - 01v2 (1080p) [112E43D1]", Identity{}, se(2, 1), nil},
		{"flattened by an indexer", "[SubsPlease].Kingdom.S5-06.1080p.[914E20BC]", Identity{}, se(5, 6), nil},
		{"a pack's file with no group", "Seirei.Gensouki.S2.-.01", Identity{}, se(2, 1), nil},
		{"the marker in words", "[Erai-raws] Kingdom 3rd Season - 26 END [1080p][Multiple Subtitle][3DC8CB0C]", Identity{}, se(3, 26), nil},
		{"through the scene table", "[SubsPlease] Show S2 - 01 (1080p) [ABCD1234]", split, se(1, 13), nil},
		{"the season the scene names give the title", "[Erai-raws].Shiguang.Dailiren.Season.3-05.[CA].[1080p.CR.WEBRip.HEVC.AAC]", named, se(4, 5), nil},
		{"a count through the series is the episode the table gives it", "[HorribleSubs] Shingeki no Kyojin S3 - 49 [1080p]", counted, se(3, 12), nil},
		{"no marker, no season", "[SubsPlease] Show - 13 (1080p) [ABCD1234]", split, se(1, 13), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			cov := CoverageFor(p, tc.id)
			require.Equal(t, tc.want, cov.Episodes)
			require.Equal(t, tc.absolute, cov.Absolute)
		})
	}
}

// A season search takes such a release for the season its title names and
// for no other: read as an absolute number, "Show S2 - 05" was season 1's
// fifth episode and none of season 2's.
func TestASeasonSearchTakesASeasonMarkedEpisodeForItsSeasonAlone(t *testing.T) {
	p, err := release.Parse("[SubsPlease] Show S2 - 05 (1080p) [ABCD1234]", release.Options{Kind: common.MediaKindEpisode})
	require.NoError(t, err)
	season := func(n, first int) Identity {
		id := Identity{SeasonSearch: true, Season: n}
		for e := 1; e <= 12; e++ {
			id.Episodes = append(id.Episodes, e)
			id.Absolute = append(id.Absolute, first+e-1)
		}
		return id
	}
	one, two := season(1, 1), season(2, 13)
	require.NotNil(t, numberingRejection(one, newIdentitySceneIndex(one), p), "season 1 is not what the title names")
	require.Nil(t, numberingRejection(two, newIdentitySceneIndex(two), p))
}
