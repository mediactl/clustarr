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
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// issueNumberRegex finds the first plain decimal token (integer or
// integer.fraction) in a provider-supplied issue number string, e.g. "12" in
// "12", "12.5" in "12.5.HU", or "1" in "Annual 1". It deliberately does not
// attempt Unicode vulgar fractions (e.g. "½") or ranges ("1-2"): ComicVine's
// issue_number field (pkg/metadata/clients/comicvine.Client.Issues) is
// observed to be plain numeric in practice, and this project has no verified
// source pinning every format the field can take, so this only handles the
// shape actually seen rather than guessing at the rest.
var issueNumberRegex = regexp.MustCompile(`\d+(?:\.\d+)?`)

// CalculatedNumberCentis derives IssueSpec's sortable numeric form of a
// provider issue number, in hundredths (design spec §4.2's IssueSpec.
// CalculatedNumber, stored as a scaled int32 per api/catalog/v1alpha1's own
// "CRD schemas do not allow floats" comment on CalculatedNumberCentis).
//
// This is Clustarr's own parsing rule, not one lifted verbatim from an *arr
// precedent -- Kapowarr's own comparable field (calculated_issue_number
// float, docs/research/quality.md:512) is documented as existing but not as
// computed by any verified algorithm. When number carries no numeric token
// at all (e.g. a purely textual special), this returns 0 rather than
// guessing a position -- the same "leave it unset rather than fail the whole
// fan-out" policy buildMovieMetadataAC uses for CollectionRef's TmdbID.
func CalculatedNumberCentis(number string) int32 {
	m := issueNumberRegex.FindString(number)
	if m == "" {
		return 0
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return 0
	}
	return int32(math.Round(f * 100))
}

// IssueName renders an Issue object's name per spec §4.2:
// "<comic>-<calculatedNumber padded 5.1>" -- a printf width.precision pair
// (width 5, one decimal digit), exactly like EpisodeName's %02d comment: a
// long-running numbering (issue 1092.0) is never truncated, only ever padded
// wider than 5 when the value itself needs more room. calculatedNumberCentis
// is IssueSpec.CalculatedNumberCentis itself (hundredths), converted back to
// the decimal the name renders.
func IssueName(comicName string, calculatedNumberCentis int32) string {
	return fmt.Sprintf("%s-%05.1f", comicName, float64(calculatedNumberCentis)/100.0)
}
