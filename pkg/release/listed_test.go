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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A series whose broadcasts each hold several of TVDB's episodes is
// released two ways (talos, 2026-11-26, Panty & Stocking with Garterbelt):
// by broadcast, the episodes in it listed after the token, and by TVDB's
// episode under that episode's own title. Both were read as "S02E07" and
// no more.
func TestTheNumbersATitleListsAfterItsEpisodeAndTheEpisodesTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		episodes    []int
		listed      []int
		epTitle     string
	}{
		{"a broadcast with the episodes it holds", "Panty.and.Stocking.with.Garterbelt.S02E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-VARYG", []int{7}, []int{14, 15}, ""},
		{"the same under the season's own title", "New.PANTY.and.STOCKING.with.GARTERBELT.S01E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.265.MSubs-ToonsHub", []int{7}, []int{14, 15}, ""},
		{"three listed", "New.PANTY.and.STOCKING.with.GARTERBELT.S01E08.16.17.18.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.265.MSubs-ToonsHub", []int{8}, []int{16, 17, 18}, ""},
		{"TVDB's episode under its title", "PANTY.and.STOCKING.with.GARTERBELT.S02E07.Daten.City.President.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-Kitsune.mkv-xpost", []int{7}, nil, "Daten City President"},
		{"a title that opens with a number lists nothing", "Some.Show.S01E03.24.Hours.Later.1080p.WEB.h264-GRP", []int{3}, nil, "24 Hours Later"},
		{"an audio layout lists nothing", "Some.Show.S01E02.5.1.720p.HDTV-GRP", []int{2}, nil, "5 1"},
		{"a year lists nothing", "Some.Show.S01E01.2019.1080p.WEB-GRP", []int{1}, nil, "2019"},
		{"no title before the quality", "Some.Show.S01E02.1080p.WEB.h264-GRP", []int{2}, nil, ""},
		{"no quality to end a title", "Some.Show.S01E02.Some.Words", []int{2}, nil, ""},
		{"a multi-episode token is not a broadcast", "Some.Show.S01E02E03.4.5.1080p.WEB-GRP", []int{2, 3}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.title, Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			assert.Equal(t, tc.episodes, p.Episodes)
			assert.Equal(t, tc.listed, p.ListedEpisodes)
			assert.Equal(t, tc.epTitle, p.EpisodeTitle)
		})
	}
}
