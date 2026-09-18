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
)

func TestParseGroupExtractsReleaseGroupEditionAndHashFallback(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		group   string
		hash    string
		edition string
	}{
		{"standard scene group", "Dune.Part.Two.2024.1080p.BluRay.x264-GROUP", "GROUP", "", ""},
		{"anime bracket group", "[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv", "SubsPlease", "", ""},
		{"8-hex hash is not a group", "Some.Movie.2020.1080p.WEB-DL.x264-a1b2c3d4", "", "a1b2c3d4", ""},
		{"directors cut edition", "Blade.Runner.1982.Directors.Cut.1080p.BluRay.x264-GROUP", "GROUP", "", "Director's Cut"},
		{"exception group with digits", "Some.Movie.2020.1080p.BluRay.x264-126811", "126811", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, hash, edition := parseGroup(tt.title)
			assert.Equal(t, tt.group, group)
			assert.Equal(t, tt.hash, hash)
			assert.Equal(t, tt.edition, edition)
		})
	}
}
