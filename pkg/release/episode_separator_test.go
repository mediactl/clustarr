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

// TestASeparatorBeforeTheEpisodeTokenIsStillAnEpisode pins the names of
// Sakamoto Days' RUBaDUB pack on talos (2026-11-27): "S01 E01" read as a
// season pack, so no file of the pack could be told from another.
func TestASeparatorBeforeTheEpisodeTokenIsStillAnEpisode(t *testing.T) {
	tests := []struct {
		name     string
		seasons  []int
		episodes []int
		full     bool
	}{
		{"[RUBaDUB][1080p] Sakamoto Days - S01 E01 [BD x265 10bit Dual Audio AC3][1663F694].mkv", []int{1}, []int{1}, false},
		{"[RUBaDUB][1080p] Sakamoto Days - S01 E22 [BD x265 10bit Dual Audio AC3][A0060132].mkv", []int{1}, []int{22}, false},
		{"Sakamoto Days S01 E02 1080p BluRay x265.mkv", []int{1}, []int{2}, false},
		{"Sakamoto.Days.S01.E03.1080p.BluRay.x265.mkv", []int{1}, []int{3}, false},
		{"Sakamoto Days S01 E04 E05 1080p BluRay x265.mkv", []int{1}, []int{4, 5}, false},
		{"Sakamoto Days S01 E01-E03 1080p BluRay x265.mkv", []int{1}, []int{1, 2, 3}, false},
		// What must stay a season pack: no episode token follows the season.
		{"Sakamoto Days S01 1080p BluRay x265 Dual Audio", []int{1}, nil, true},
		{"Sakamoto Days S01 Extras 1080p BluRay x265", []int{1}, nil, true},
		{"Sakamoto.Days.S01.EAC3.1080p.BluRay.x265", []int{1}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.name, Options{})
			require.NoError(t, err)
			assert.Equal(t, tt.seasons, p.Seasons)
			assert.Equal(t, tt.episodes, append([]int(nil), p.Episodes...))
			assert.Equal(t, tt.full, p.FullSeason)
		})
	}
}
