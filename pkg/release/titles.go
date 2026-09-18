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
	"strings"

	"github.com/moistari/rls"
)

// akaSplitRegex splits a cleaned (separator-normalized) title on an
// "AKA"/"aka" or "/" alternate-title marker. Plain stdlib regexp, not
// regexp2: no lookaround needed, matching normalize.go's helper regexes.
var akaSplitRegex = regexp.MustCompile(`(?i)\s+aka\s+|\s*/\s*`)

// rlsAltTitle asks moistari/rls for its own alternate-title extraction
// (Release.Alt) — a separate, distinct concept from the Hints this package
// otherwise takes from rls (hints.go): those are custom-format tags, this
// is a second title. rls recognizes " AKA " itself but not lowercase " aka "
// or " / ", which is why buildTitles below also runs akaSplitRegex.
func rlsAltTitle(title string) string {
	return strings.TrimSpace(rls.ParseString(title).Alt)
}

// buildTitles splits a cleaned primary-title string on any AKA/aka/slash
// alternate-title marker, then merges in rls's own alternate-title
// extraction if it found one this package's own split didn't already
// surface. The result always has the primary title first and at least one
// entry — "Titles always contains Title first."
func buildTitles(cleanedTitle, rawTitle string) []string {
	parts := akaSplitRegex.Split(cleanedTitle, -1)
	titles := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			titles = append(titles, p)
		}
	}
	if len(titles) == 0 {
		titles = []string{cleanedTitle}
	}

	if alt := rlsAltTitle(rawTitle); alt != "" {
		found := false
		for _, t := range titles {
			if strings.EqualFold(t, alt) {
				found = true
				break
			}
		}
		if !found {
			titles = append(titles, alt)
		}
	}

	return titles
}
