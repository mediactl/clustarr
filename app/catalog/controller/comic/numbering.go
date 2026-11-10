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

package comic

import (
	"regexp"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// volumeNumberRegex reads the volume a collected edition's number or title
// names: "Vol. 3", "Volume 3", "Vol 03", "v03", "Tome 3", "Book 3", "Tomo
// 3", "Band 3", with or without a trailing ":" or "-" and whatever follows.
// A bare number is not a volume spelling; VolumeIssueNumber leaves it as
// it is.
var volumeNumberRegex = regexp.MustCompile(`(?i)(?:^|[\s(\[])(?:vol(?:ume)?\.?|v|tome|tomo|book|band)\s*0*(\d+)(?:\b|$)`)

// VolumeNumber is the volume number a collected edition's number or title
// spells out ("Vol. 3" is "3", "v03" is "3"), and whether either does. The
// number is read first, then the title. Leading zeroes go, so "Vol. 03" and
// "Volume 3" are the one issue.
func VolumeNumber(number, title string) (string, bool) {
	for _, s := range []string{number, title} {
		m := volumeNumberRegex.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		n := strings.TrimLeft(m[1], "0")
		if n == "" {
			n = "0"
		}
		return n, true
	}
	return "", false
}

// VolumeIssueNumber is the number an Issue takes under the comic's
// specialVersion (2026-11-10 design D10): for volumeAsIssue the Issues are
// numbered by volume, Mylar's rule for a title collected as volumes
// (manga tankōbon, a trade-paperback run), so an issue whose provider
// number or title spells a volume ("Vol. 3", "Volume 3", "v03") is issue
// "3"; a plain number is already the volume's and passes through, as does
// a number spelling no volume. MangaDex lists a manga's volumes as its
// issues already numbered by volume, and ComicVine numbers a collected
// edition's volumes 1..N in most listings, so the common case is the
// pass-through; this reads the ones it spells out. Every other special
// version keeps the provider's number as it is.
//
// The number is read at the fan-out, before IssueName, so a renumbered
// issue is a new Issue object: an Issue's number is immutable (IssueSpec).
func VolumeIssueNumber(spec catalogv1alpha1.ComicSpec, number, title string) string {
	if spec.SpecialVersion != catalogv1alpha1.ComicSpecialVersionVolumeAsIssue {
		return number
	}
	if n, ok := VolumeNumber(number, title); ok {
		return n
	}
	return number
}
