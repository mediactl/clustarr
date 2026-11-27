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

// A release the scene table places at an episode is that episode, and no
// other one that happens to share a number with it. Link Click on talos,
// 2026-11-27: TheXEM's row for the scene's season 3 episode 5 is TVDB's
// S04E05 with absolute number 34, while the series stores S04E05 as 29 and
// S03E10 as 34. The row's absolute number rode along in the coverage, so a
// search of season 3 took the release as one of its episodes, a search for
// S03E10 as that episode, and the planner grabbed it for S03E10, which was
// missing; the importer filed it as S04E05 and held it against the
// transcoded file there, four releases in four minutes.
func TestASceneRowNamesItsEpisodeByPlaceAlone(t *testing.T) {
	const title = "[Erai-raws].Shiguang.Dailiren.Season.3-05.[CA].[1080p.CR.WEBRip.HEVC.AAC][MultiSub][402F2D86]"
	scene := []SceneMapping{{
		Scene: EpisodeNumbering{Season: 3, Episode: 5, Absolute: 34},
		TVDB:  EpisodeNumbering{Season: 4, Episode: 5, Absolute: 34},
	}}
	p, err := release.Parse(title, release.Options{Kind: common.MediaKindEpisode})
	require.NoError(t, err)
	require.Equal(t, []int{5}, p.Absolute)
	require.Equal(t, 3, p.TitleSeason)

	cov := CoverageOf(p, scene)
	require.Equal(t, []SeasonEpisode{{Season: 4, Episode: 5}}, cov.Episodes)
	require.Empty(t, cov.Absolute, "the row's absolute number is TheXEM's count, not the series'")

	// The series' stored numbers: season 3's last six episodes and season
	// 4's share 31-36.
	season := func(n int, episodes, absolute []int) Identity {
		return Identity{SeasonSearch: true, Season: n, Episodes: episodes, Absolute: absolute, SceneMappings: scene}
	}
	three := season(3, []int{7, 8, 9, 10, 11, 12}, []int{31, 32, 33, 34, 35, 36})
	four := season(4, []int{1, 2, 3, 4, 5, 6}, []int{25, 26, 27, 28, 29, 30})
	require.NotNil(t, numberingRejection(three, newIdentitySceneIndex(three), p), "season 3 holds no episode of the release")
	require.Nil(t, numberingRejection(four, newIdentitySceneIndex(four), p))

	missing := Identity{Season: 3, Episodes: []int{10}, Absolute: []int{34}, SceneMappings: scene}
	require.NotNil(t, numberingRejection(missing, newIdentitySceneIndex(missing), p), "S03E10 is not the release's episode")
	has := Identity{Season: 4, Episodes: []int{5}, Absolute: []int{29}, SceneMappings: scene}
	require.Nil(t, numberingRejection(has, newIdentitySceneIndex(has), p))
}
