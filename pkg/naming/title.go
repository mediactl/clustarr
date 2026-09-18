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

package naming

import "strings"

var apostropheStripper = strings.NewReplacer("'", "", "\xe2\x80\x99", "")

// cleanTitle strips apostrophes (straight and curly) from title, following
// the *arr CleanTitle convention: punctuation that is unsafe or noisy in a
// filename is stripped, but characters like '!' are kept verbatim.
func cleanTitle(title string) string { return apostropheStripper.Replace(title) }

// titleThe moves a leading "The " to a trailing ", The" -- the *arr
// TitleThe token, used by media servers that sort by a title's first
// significant word.
func titleThe(title string) string {
	const prefix = "The "
	if strings.HasPrefix(title, prefix) {
		return title[len(prefix):] + ", The"
	}
	return title
}

// replaceColon applies one of the five ColonReplacement modes to s. The
// zero value and ColonSmart both use the "smart" rule: ": " (colon +
// space) becomes " - ", and any remaining bare colon becomes "-".
func replaceColon(s string, mode ColonReplacement) string {
	switch mode {
	case ColonDelete:
		return strings.ReplaceAll(s, ":", "")
	case ColonDash:
		return strings.ReplaceAll(s, ":", "-")
	case ColonSpaceDash:
		return strings.ReplaceAll(s, ":", " -")
	case ColonSpaceDashSpace:
		// Consume a colon-plus-space run first, else ": " + the mode's own
		// trailing space would double up (e.g. "Man:  Into" instead of
		// "Man - Into"); a bare colon with no following space still gets
		// " - " from the second pass.
		s = strings.ReplaceAll(s, ": ", " - ")
		return strings.ReplaceAll(s, ":", " - ")
	default: // ColonSmart and the zero value
		s = strings.ReplaceAll(s, ": ", " - ")
		return strings.ReplaceAll(s, ":", "-")
	}
}
