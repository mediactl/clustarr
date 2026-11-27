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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A season released in parts is posted as one pack per part. Each is a pack
// of the episodes its title states, in either series type's reading, and a
// pack that states none is still the whole season.
func TestASeasonPackThatStatesItsEpisodesIsAPackOfThose(t *testing.T) {
	tests := []struct {
		title    string
		season   int
		episodes []int
		full     bool
	}{
		{"[EMBER] How a Realist Hero Rebuilt the Kingdom (2022) (Season 1 | Part 02 [EP: 14-26]) [BDRip] [1080p Dual Audio HEVC 10 bits]", 1, intRange(14, 26), false},
		{"[EMBER] How a Realist Hero Rebuilt the Kingdom (2021) (Season 1 | Part 01 [EP: 01-13]) [BDRip] [1080p Dual Audio HEVC 10 bits]", 1, intRange(1, 13), false},
		{"[EMBER] How a Realist Hero Rebuilt the Kingdom (2022) (Season 1 Part 02 [EP: 14-26]) [1080p] [Dual Audio HEVC WEBRip]", 1, intRange(14, 26), false},
		{"[Group] Some Show (Season 2) (Episodes 1~12) [BD 1080p]", 2, intRange(1, 12), false},
		// Both parts in one pack, a part that names no episodes, and a plain
		// season: the whole season, as before.
		{"[EMBER] How a Realist Hero Rebuilt the Kingdom (2021-2022) (Season 1 [Part 01+Part 02]) [BDRip] [1080p Dual Audio HEVC 10 bits]", 1, nil, true},
		{"How a Realist Hero Rebuilt the Kingdom - S01 (BDRip 1920x1080 x264 AAC-FLAC) [Dual-Audio]", 1, nil, true},
		// A range of seasons is not a range of episodes, nor is a pack of
		// two seasons narrowed by one.
		{"How.a.Realist.Hero.Rebuilt.the.Kingdom.S01-S02.1080p.Blu-Ray.Dual-Audio.TrueHD.x265-iAHD", 1, nil, true},
	}
	for _, seriesType := range []string{"standard", "anime"} {
		for _, tt := range tests {
			t.Run(seriesType+"/"+tt.title, func(t *testing.T) {
				p, err := Parse(tt.title, Options{Kind: "episode", SeriesType: seriesType})
				require.NoError(t, err)
				assert.Equal(t, tt.episodes, p.Episodes)
				assert.Equal(t, tt.full, p.FullSeason)
				assert.Equal(t, tt.episodes != nil, p.Partial)
				assert.Contains(t, p.Seasons, tt.season)
			})
		}
	}
}

func TestOnlyAWordedRangeStatesEpisodes(t *testing.T) {
	for title, want := range map[string][2]int{
		"Show (Season 1) [EP: 14-26]":          {14, 26},
		"Show Season 1 Eps 001-012 [1080p]":    {1, 12},
		"Show (Season 1) [1920-1080]":          {},
		"Show (2021-2022) (Season 1)":          {},
		"Show (Season 1) [EP: 26-14]":          {},
		"Show (Season 1) [EP: 05]":             {},
		"Show Deep 01-13 (Season 1)":           {},
		"Show (Season 1) [Episodes 1-9000]":    {},
		"Show (Season 1) (Episode 12-13 v2)":   {12, 13},
		"Show (Season 1) [steps 01-13] [1080]": {},
	} {
		first, last, ok := statedEpisodes(title)
		assert.Equal(t, want != [2]int{}, ok, title)
		assert.Equal(t, want, [2]int{first, last}, title)
	}
}
