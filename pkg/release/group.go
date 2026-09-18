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
	"strings"

	"github.com/dlclark/regexp2"
)

// animeGroupRegex ports AnimeReleaseGroupRegex verbatim: a bracketed
// sub-group token anchored at the very start of the title.
var animeGroupRegex = mustCompile(`^(?:\[(?<subgroup>(?!\s).+?(?<!\s))\](?:_|-|\s|\.)?)`, regexp2.IgnoreCase)

// exceptionGroupRegex ports a representative subset of
// ExceptionReleaseGroupRegexExact: scene/P2P group names that don't follow
// the standard trailing "-GROUP" convention (some punctuate with a dot, like
// "x264.YIFY"), so they must be recognized by name rather than by position.
var exceptionGroupRegex = mustCompile(
	`\b(?<group>KRaLiMaRKo|BluDragon|HQMUX|VARYG|YIFY|YTS(?:\.(?:MX|LT|AG))?|TMd|LMain|DarQ|QxR|126811)\b`,
	regexp2.IgnoreCase,
)

// releaseGroupRegex ports the common case of ReleaseGroupRegex: a release
// group as the trailing "-GROUP" token at the end of the title.
var releaseGroupRegex = mustCompile(`-(?<group>[A-Za-z0-9]+(?:-[A-Za-z0-9]+)?)$`, regexp2.IgnoreCase)

// invalidGroupRegex ports InvalidReleaseGroupRegex: a trailing token that
// looks like a group but is actually a season/episode marker or an 8-hex
// scene-obfuscation hash.
var invalidGroupRegex = mustCompile(`^([se]\d+|[0-9a-f]{8})$`, regexp2.IgnoreCase)

// editionRegex ports the token alternation EditionRegex matches against.
var editionRegex = mustCompile(
	`\b(Director'?s[.\s]?Cut|Extended[.\s]?(?:Cut|Edition)?|Theatrical(?:[.\s]Cut)?|Unrated|`+
		`Remastered|IMAX|Uncut|Ultimate[.\s]?(?:Cut|Edition)?|Special[.\s]?Edition)\b`,
	regexp2.IgnoreCase,
)

// editionCanonical normalizes the handful of edition tokens the fixture
// corpus and EditionRegex's alternation exercise to their canonical display
// form. The lookup key strips punctuation/whitespace and lower-cases, so
// "Directors.Cut", "director's cut" and "DIRECTORS CUT" all collapse to the
// same entry.
var editionCanonical = map[string]string{
	"directorscut":    "Director's Cut",
	"extended":        "Extended",
	"extendedcut":     "Extended",
	"extendededition": "Extended",
	"theatrical":      "Theatrical",
	"theatricalcut":   "Theatrical",
	"unrated":         "Unrated",
	"remastered":      "Remastered",
	"imax":            "IMAX",
	"uncut":           "Uncut",
	"ultimate":        "Ultimate",
	"ultimatecut":     "Ultimate",
	"ultimateedition": "Ultimate",
	"specialedition":  "Special Edition",
}

func editionLookupKey(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseEdition matches editionRegex and normalizes the captured token to its
// canonical form via editionCanonical, falling back to punctuation-to-space
// normalization for a token the table doesn't cover.
func parseEdition(title string) string {
	m, err := editionRegex.FindStringMatch(title)
	if err != nil || m == nil {
		return ""
	}
	raw := m.String()
	if canonical, ok := editionCanonical[editionLookupKey(raw)]; ok {
		return canonical
	}
	return strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ").Replace(raw))
}

// parseGroup extracts the release group, scene-obfuscation hash fallback and
// edition from a release title. Dispatch order: anime bracket-group first
// (it anchors at the start, independent of anything a trailing-dash group
// might also claim), then the named-exception list, then the generic
// trailing-dash pattern — only the generic path's capture is checked against
// invalidGroupRegex and demoted to hash, since neither the anime nor the
// exception path can produce a false positive of that shape.
func parseGroup(title string) (group, hash, edition string) {
	if m, err := animeGroupRegex.FindStringMatch(title); err == nil && m != nil {
		if g := m.GroupByName("subgroup"); g != nil && len(g.Captures) > 0 {
			group = g.String()
		}
	}

	if group == "" {
		if m, err := exceptionGroupRegex.FindStringMatch(title); err == nil && m != nil {
			if g := m.GroupByName("group"); g != nil && len(g.Captures) > 0 {
				group = g.String()
			}
		}
	}

	if group == "" {
		if m, err := releaseGroupRegex.FindStringMatch(title); err == nil && m != nil {
			if g := m.GroupByName("group"); g != nil && len(g.Captures) > 0 {
				candidate := g.String()
				if invalid, ierr := invalidGroupRegex.MatchString(candidate); ierr == nil && invalid {
					hash = candidate
				} else {
					group = candidate
				}
			}
		}
	}

	edition = parseEdition(title)
	return group, hash, edition
}
