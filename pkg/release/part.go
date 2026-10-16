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
	"regexp"
	"strconv"
)

// partRE is a part token -- Part1, Part.2, Part 3, Pt4, CD1, Disc 2,
// Disk.1, Part02 -- a whole token numbered 1 to 9: its leading separator,
// its number and its trailing separator.
var partRE = regexp.MustCompile(`(?i)(^|[ ._\-\[(])(?:part|pt|cd|disc|disk)[ ._\-]?0?([1-9])($|[ ._\-\])])`)

// PartOf reads the last part token of name, a release title or a file's
// base name: the part's number, and name without the token (one separator
// kept where it sat between two others). A movie released in parts carries
// one (28.Up.1984.Part1.1080p.BluRay.x264-USURY); whether the token is a
// part of the film or of its title ("Che.Part.1.2008") is the caller's to
// judge.
func PartOf(name string) (base string, part int, ok bool) {
	ms := partRE.FindAllStringSubmatchIndex(name, -1)
	if len(ms) == 0 {
		return "", 0, false
	}
	m := ms[len(ms)-1]
	n, err := strconv.Atoi(name[m[4]:m[5]])
	if err != nil {
		return "", 0, false
	}
	lead, trail := name[m[2]:m[3]], name[m[6]:m[7]]
	keep := ""
	if lead != "" && trail != "" {
		keep = lead
	}
	return name[:m[0]] + keep + name[m[1]:], n, true
}
