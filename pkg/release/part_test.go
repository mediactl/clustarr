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
package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

// The part token of a release or file name, and the name without it: real
// titles from the owner's indexers (2026-10-16), and names that only look
// like parts.
func TestPartOfReadsThePartAndTheNameWithoutIt(t *testing.T) {
	for name, want := range map[string]struct {
		base string
		part int
	}{
		"56.Up.2012.Part1.1080p.BluRay.x264-USURY":                           {"56.Up.2012.1080p.BluRay.x264-USURY", 1},
		"56.Up.2012.Part3.1080p.BluRay.x264-USURY-xpost":                     {"56.Up.2012.1080p.BluRay.x264-USURY-xpost", 3},
		"No.Direction.Home.Bob.Dylan.2005.Part.1.1080p.BluRay.x264-RedBlade": {"No.Direction.Home.Bob.Dylan.2005.1080p.BluRay.x264-RedBlade", 1},
		"No.Direction.Home.Bob.Dylan.2005.Part.2.1080p.BluRay.x264-RedBlade-Obfuscated": {
			"No.Direction.Home.Bob.Dylan.2005.1080p.BluRay.x264-RedBlade-Obfuscated", 2,
		},
		"Movie (1999) - pt3":              {"Movie (1999) -", 3},
		"Movie.1999.CD1.DVDRip.XviD":      {"Movie.1999.DVDRip.XviD", 1},
		"Movie 1999 Disc 2":               {"Movie 1999", 2},
		"28.Up.1984.Part02.720p.BluRay":   {"28.Up.1984.720p.BluRay", 2},
		"The.Departed.2006.1080p.BluRay":  {},
		"The.Apartment.1960.1080p.BluRay": {},
		"Partisan.2015.1080p":             {},
		"Dune.Part.Two.2024.1080p.WEB-DL": {},
		"Movie.Part10.2001":               {},
	} {
		base, part, ok := release.PartOf(name)
		if want.part == 0 {
			assert.Falsef(t, ok, "%s", name)
			continue
		}
		if assert.Truef(t, ok, "%s", name) {
			assert.Equalf(t, want.part, part, "%s", name)
			assert.Equalf(t, want.base, base, "%s", name)
		}
	}
}
