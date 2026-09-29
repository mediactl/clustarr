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

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// cleanTitle is Radarr's FileNameBuilder.CleanTitle, which Sonarr shares:
//
//   - "&" becomes "and", and "/" a space (ScenifyReplaceChars);
//   - ScenifyRemoveChars drops a lone mark between two spaces (" - ",
//     " : "), an apostrophe, backtick, colon, question mark or comma
//     followed by a space, the end, or a contraction's ending ("'s ",
//     "'t ", "'ll "...), and every bracket;
//   - diacritics go ("Caché" -> "Cache"), as RemoveDiacritics;
//   - a run of one separator collapses to one, as the file-name builder's
//     FileNameCleanupRegex does ("if...." -> "if.").
//
// Everything else stays, "!" and "+" and "-" inside a word included. A
// colon inside a word ("GANTZ:O") is left for ColonReplacement.
// TestCleanTitleIsRadarrs holds it to every title Radarr wrote into the
// owner's library.
func cleanTitle(title string) string {
	rs := []rune(scenifyReplacer.Replace(title))
	var b strings.Builder
	for i, r := range rs {
		spaceBefore := i > 0 && unicode.IsSpace(rs[i-1])
		spaceAfter := i+1 < len(rs) && unicode.IsSpace(rs[i+1])
		switch {
		case spaceBefore && spaceAfter && strings.ContainsRune(loneMarks, r):
			continue
		case strings.ContainsRune(trailingMarks, r) && (i+1 == len(rs) || spaceAfter || contractionFollows(rs[i+1:])):
			continue
		case strings.ContainsRune("()[]{}", r):
			continue
		}
		b.WriteRune(r)
	}
	return collapseSeparators(removeDiacritics(b.String()))
}

var scenifyReplacer = strings.NewReplacer("&", "and", "/", " ")

const (
	loneMarks     = ",<>/\\;:'\"|`\u2019~!?@$%^*-_="
	trailingMarks = "'`\u2019:?,"
)

// contractionFollows reports whether rest starts with a contraction's
// ending and a space: "s ", "m ", "t ", "ve ", "ll ", "d " or "re ",
// in any case.
func contractionFollows(rest []rune) bool {
	for _, end := range []string{"s", "m", "t", "ve", "ll", "d", "re"} {
		n := len(end)
		if len(rest) > n && strings.EqualFold(string(rest[:n]), end) && unicode.IsSpace(rest[n]) {
			return true
		}
	}
	return false
}

func removeDiacritics(s string) string {
	out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		return s
	}
	return out
}

// collapseSeparators replaces a run of one of "- ._" with a single one.
func collapseSeparators(s string) string {
	var b strings.Builder
	var prev rune
	for i, r := range s {
		if i > 0 && r == prev && strings.ContainsRune("- ._", r) {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

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
