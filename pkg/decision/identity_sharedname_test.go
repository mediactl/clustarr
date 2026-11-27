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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// newGame is TheXEM's table for New Game! (TVDB 309532), as it answered on
// 2026-11-27: two twelve-episode seasons the scene counts from 1 each and
// TVDB counts through (season 2 opens at 14), and names for both seasons
// that differ in one exclamation mark.
func newGame() Identity {
	id := Identity{SceneSeasonNames: []SceneSeasonName{
		{Title: "New Game!", Season: 1},
		{Title: "New Game!!", Season: 2},
		{Title: "New Game! S2", Season: 2},
		{Title: "New Game!! S2", Season: 2},
	}}
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			id.SceneMappings = append(id.SceneMappings, SceneMapping{
				Scene: EpisodeNumbering{Season: s, Episode: n, Absolute: n},
				TVDB:  EpisodeNumbering{Season: s, Episode: n, Absolute: (s-1)*13 + n},
			})
		}
	}
	return id
}

// Two seasons' scene names that differ in their punctuation alone each name
// their own season, read as the search and import agents parse a title.
// Keyed without punctuation the later name won, and every release of the
// first season was the second's: "[DB]New Game! - 09" covered S02E09 (found
// replaying talos' files, 2026-11-27).
func TestSceneNamesTwoSeasonsShareAreToldApartBySpelling(t *testing.T) {
	id := newGame()
	for _, tc := range []struct {
		title string
		want  SeasonEpisode
		named int
	}{
		{"[DB]New Game! - 09 (Dual Audio 10bit BD1080p x265)", SeasonEpisode{Season: 1, Episode: 9}, 0},
		{"[Cleo]New Game!! - 01 (Dual Audio 10bit BD1080p x265)", SeasonEpisode{Season: 2, Episode: 1}, 2},
		{"[Group] New Game! S2 - 03 [1080p]", SeasonEpisode{Season: 2, Episode: 3}, 2},
	} {
		t.Run(tc.title, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			cov := CoverageFor(p, id)
			assert.Equal(t, []SeasonEpisode{tc.want}, cov.Episodes)
			assert.Empty(t, cov.Absolute)
			assert.Equal(t, tc.named, NamedSeason(p, id), "the season the title names")
		})
	}
}

// The season a title names is its marker's, or the later season it is the
// series' scene name of; the series' own title and its first season's name
// name none.
func TestNamedSeason(t *testing.T) {
	id := Identity{SceneSeasonNames: []SceneSeasonName{
		{Title: "Date A Live S2", Season: 2},
		{Title: "Date A Live II", Season: 2},
		{Title: "Date a Live IV", Season: 4},
		{Title: "Shingeki no Bahamut: Genesis", Season: 1},
	}}
	for title, want := range map[string]int{
		"[Cleo]Date A Live II - 07 (Dual Audio 10bit BD1080p x265)":          2,
		"[Cleo]Date A Live IV - 11 (Dual Audio 10bit 1080p x265)":            4,
		"[Cleo]Date A Live - 07 (Dual Audio 10bit BD1080p x265)":             0,
		"[Erai-raws] Shingeki no Bahamut - Genesis - 01 [1080p]":             0,
		"[SubsPlease] Pop Team Epic S2 - 01v2 (1080p) [112E43D1]":            2,
		"[Erai-raws] Kingdom 3rd Season - 26 END [1080p][Multiple Subtitle]": 3,
	} {
		t.Run(title, func(t *testing.T) {
			p, err := release.Parse(title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			assert.Equal(t, want, NamedSeason(p, id))
		})
	}
	assert.Zero(t, NamedSeason(nil, id))
}
