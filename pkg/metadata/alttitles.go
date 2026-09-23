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
package metadata

import (
	"strings"
	"unicode"
)

// DistinctAltTitles is how a provider client files the titles it maps into
// Movie.AlternateTitles or Series.AlternateTitles, in the order given: a
// blank title is dropped, a title that is the entity's own primary title is
// dropped, and of two titles that are the same only the first is kept.
// Two titles are the same when their letters and digits match, ignoring
// case, so "Juego de Tronos" and "juego de tronos." are one title.
//
// This is Radarr's AlternativeTitleService.UpdateTitles (develop c90668a5):
// it drops a title whose clean title is the movie's, then keeps one title
// per clean title. Radarr's clean title also strips articles and a few
// words, which this narrower key does not, so a pair Radarr would fold --
// "The Origin" and "Origin" -- is kept as two. That is harmless: every
// consumer normalises a title again before it compares one.
func DistinctAltTitles(primary string, titles []AltTitle) []AltTitle {
	seen := map[string]bool{altTitleKey(primary): true}
	var out []AltTitle
	for _, t := range titles {
		t.Title = strings.TrimSpace(t.Title)
		if t.Title == "" {
			continue
		}
		key := altTitleKey(t.Title)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// altTitleKey is a title's letters and digits, lower-cased. A title with
// neither -- all punctuation -- keys as itself, so it is compared exactly
// rather than colliding with every other such title.
func altTitleKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	if b.Len() == 0 {
		return strings.TrimSpace(s)
	}
	return b.String()
}
