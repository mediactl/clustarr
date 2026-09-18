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

// multiRegex ports *arr's MultiRegex: a MULTi token short-circuits language
// detection to "Original" (the release carries the original-language track
// alongside others, rather than a single named language).
var multiRegex = mustCompile(`[_.\s]multi[_.\s]`, regexp2.IgnoreCase)

// languageRegex is a bounded token table covering the languages the fixture
// corpus exercises, not the full ~50-entry Radarr language id list
// (docs/research/quality.md line 397): porting all of them is real work with
// no behavioral payoff until pkg/decision's language-profile matching
// exists to consume the extra entries, and the table is additive to extend
// later, not a breaking change to parseLanguages's signature.
var languageRegex = mustCompile(
	`\b(?<French>FRENCH|VFF|VFQ)\b|\b(?<German>GERMAN)\b|\b(?<Spanish>SPANISH)\b|`+
		`\b(?<Italian>ITALIAN)\b|\b(?<Japanese>JAPANESE)\b|\b(?<Korean>KOREAN)\b`,
	regexp2.IgnoreCase,
)

var languageGroups = []string{"French", "German", "Spanish", "Italian", "Japanese", "Korean"}

// parseLanguages extracts the language tags from a release title. MULTi
// short-circuits to ["Original"]; no match defaults to ["English"], mirroring
// *arr's own LanguageParser default.
func parseLanguages(title string) []string {
	if ok, err := multiRegex.MatchString(title); err == nil && ok {
		return []string{"Original"}
	}

	if m, err := languageRegex.FindStringMatch(title); err == nil && m != nil {
		for _, name := range languageGroups {
			if grp := m.GroupByName(name); grp != nil && len(grp.Captures) > 0 {
				return []string{name}
			}
		}
	}

	return []string{"English"}
}
