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

import "github.com/dlclark/regexp2"

// languageRegex is a bounded token table covering the languages the fixture
// corpus exercises, not the full ~50-entry Radarr language id list
// (docs/research/quality.md line 397): porting all of them is real work with
// no behavioral payoff until pkg/decision's language-profile matching
// exists to consume the extra entries, and the table is additive to extend
// later, not a breaking change to parseLanguages's signature.
// The Chinese alternation adds CHS/CHT (simplified/traditional scene
// encoding tokens), GB/BIG5 (the same distinction under their other common
// spelling), the full word "Chinese", and the CJK literal "中文" -- the
// scene-token and human-language forms a Chinese release actually carries,
// so that a detected Chinese language can satisfy the anime-dual-audio
// custom format's Language Kind-group (Radarr language id 10; see
// pkg/quality/catalogue/languages.go's own note that this table's missing
// Chinese entry was exactly why that group could never fire for a Chinese
// release). \b keeps GB (two ASCII letters) from matching inside an
// unrelated token like a "1GB" size suffix.
var languageRegex = mustCompile(
	`\b(?<French>FRENCH|VFF|VFQ)\b|\b(?<German>GERMAN)\b|\b(?<Spanish>SPANISH)\b|`+
		`\b(?<Italian>ITALIAN)\b|\b(?<Japanese>JAPANESE)\b|\b(?<Korean>KOREAN)\b|`+
		`\b(?<Chinese>CHS|CHT|GB|BIG5|Chinese|中文)\b`,
	regexp2.IgnoreCase,
)

var languageGroups = []string{"French", "German", "Spanish", "Italian", "Japanese", "Korean", "Chinese"}

// parseLanguages extracts the language tags from a release title. No match
// defaults to ["English"].
//
// A "MULTi" token is deliberately not a language. Radarr's semantics, from
// its source (develop, fetched 2026-09-23): LanguageParser.ParseLanguages
// has no MULTi rule at all, so "Movie.2020.MULTi.1080p" parses exactly as
// "Movie.2020.1080p" does and "Movie.2020.FRENCH.MULTi.1080p" as French.
// MULTi is read in one place only, AggregateLanguages, and only to add the
// languages the indexer's per-indexer "Multi Languages" setting names
// (Parser.HasMultipleLanguages, MultiRegex `[_. ](?<multi>multi)[_. ]`).
// Clustarr's Indexer has no such setting, so MULTi contributes nothing here
// either. This used to return ["Original"] -- a pseudo-language no
// condition or profile could compare against the item's original language,
// so a MULTi release of an English-original film failed
// language-not-original and an "original" language profile alike.
//
// One divergence from Radarr stands and is not MULTi's: a title with no
// language token is ["English"] here, where Radarr parses Unknown and
// AggregateLanguages then substitutes the item's original language. That
// substitution needs the item, which this item-independent parse does not
// have.
func parseLanguages(title string) []string {
	if m, err := languageRegex.FindStringMatch(title); err == nil && m != nil {
		for _, name := range languageGroups {
			if grp := m.GroupByName(name); grp != nil && len(grp.Captures) > 0 {
				return []string{name}
			}
		}
	}

	return []string{"English"}
}
