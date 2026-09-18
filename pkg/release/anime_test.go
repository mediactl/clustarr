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

func TestParseSeriesAnimeAbsoluteAndBracketGroup(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		seriesTitle string
		absolute    []int
		group       string
	}{
		// quality.md §7.3: rls drops the group entirely here.
		{
			"subsplease absolute", "[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv",
			"Frieren",
			[]int{28},
			"SubsPlease",
		},
		// quality.md §7.3: rls mis-sets group to "ARA" (a language token) here.
		{
			"erai-raws absolute", "[Erai-raws] One Piece - 1090 [1080p][Multiple Subtitle].mkv",
			"One Piece",
			[]int{1090},
			"Erai-raws",
		},
		{
			"season plus absolute", "[SubsPlease] Attack on Titan - S04E28 (1080p) [ABCD1234].mkv",
			"Attack on Titan", nil, "SubsPlease",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "anime"})
			require.NoError(t, err)
			assert.Equal(t, tt.seriesTitle, p.Title)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.Equal(t, tt.group, p.Group)
		})
	}
}
