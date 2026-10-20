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
	"slices"
	"strings"

	"github.com/dlclark/regexp2"
)

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
	`\b(?<English>ENGLISH)\b|`+
		`\b(?<French>FRENCH|TRUEFRENCH|VFF|VFQ|VFI|VF2|VF)\b|\b(?<German>GERMAN)\b|\b(?<Spanish>SPANISH)\b|`+
		`\b(?<Italian>ITALIAN)\b|\b(?<Japanese>JAPANESE)\b|\b(?<Korean>KOREAN)\b|`+
		`\b(?<Chinese>CHS|CHT|GB|BIG5|Chinese|中文)\b`,
	regexp2.IgnoreCase,
)

// languageCodePairRegex is a run of two-letter audio codes joined by "+":
// TRaSH's JA+EN form (the anime-dual-audio custom format) and the one
// Sonarr's {MediaInfo AudioLanguages} renames a file with. A code alone is
// no language -- "[JA]" is too short to trust outside such a run.
var languageCodePairRegex = mustCompile(`\b(?:JA|ZH|KO|EN)(?: ?\+ ?(?:JA|ZH|KO|EN))+\b`, regexp2.IgnoreCase)

// languageCodes names languageCodePairRegex's codes.
var languageCodes = map[string]string{"JA": "Japanese", "ZH": "Chinese", "KO": "Korean", "EN": "English"}

// languageCode3s are the ISO 639-2 audio codes a title names its languages
// by ("ENG.ITA", "JPN.ENG"), each the catalogue's display name. Codes that
// are also words or names (DAN, FIN, NOR, ARA, THA, VIE, SPA) are left out.
var languageCode3s = map[string]string{
	"ENG": "English", "ITA": "Italian", "JPN": "Japanese", "JAP": "Japanese",
	"GER": "German", "DEU": "German", "FRE": "French", "FRA": "French",
	"ESP": "Spanish", "KOR": "Korean", "CHI": "Chinese", "ZHO": "Chinese",
	"POR": "Portuguese", "RUS": "Russian", "HIN": "Hindi", "TUR": "Turkish",
	"POL": "Polish", "DUT": "Dutch", "NLD": "Dutch", "SWE": "Swedish",
	"HUN": "Hungarian", "CZE": "Czech", "GRE": "Greek",
}

// languageCode3Regex is a run of two or more languageCode3s, a title that
// names its languages explicitly ("Movie.2026.ENG.ITA.1080p",
// "Show.S01E01.JPN.ENG"). A code alone is no language: too short to trust.
var languageCode3Regex = mustCompile(
	`\b(?:`+code3Alternation()+`)(?:[ ._+-]+(?:`+code3Alternation()+`))+\b`, regexp2.IgnoreCase)

func code3Alternation() string {
	codes := make([]string, 0, len(languageCode3s))
	for c := range languageCode3s {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return strings.Join(codes, "|")
}

// germanOriginalRegex and frenchOriginalRegex are the scene tags that name a
// release's original-language track without saying which language it is
// (Sonarr's and Radarr's "Original"): German beside DL (dual language; never
// WEB-DL's DL) or ML (multi language), and French beside VO (version
// originale) or MULTi. LanguagesFor resolves it to the item's original.
var (
	germanOriginalRegex = mustCompile(`(?<!WEB[ ._-]?)\bDL\b|\bML\b`, regexp2.IgnoreCase)
	frenchOriginalRegex = mustCompile(`\bVO\b|\bMULTI\b`, regexp2.IgnoreCase)
)

// dualAudioRegex is a title that says it carries English beside another
// language without naming it: "Dual Audio" in any spelling, a bracketed
// DUAL, or a bare DUAL after the resolution (TRaSH's anime-dual-audio
// condition) or right after the episode or the year. Those places keep a
// series called "Dual!" and an episode titled "The Dual" out; WEB-DL's DL
// is not a marker at all.
var dualAudioRegex = mustCompile(
	`dual[ ._-]?audio|[([]dual[])]|`+
		`\b(?:\d{3,4}[pi]|4K|U(?:ltra)?HD)\b.*\bDUAL\b|`+
		`\b(?:S\d{1,3}E\d{1,4}(?:-?E\d{1,4})*|(?:19|20)\d{2}\)?)[ ._-]+DUAL\b`,
	regexp2.IgnoreCase,
)

// dubbedRegex is a dub: a title that names no language with it is an
// English dub, the convention of the English-language indexers this reads.
var dubbedRegex = mustCompile(`\bDUBBED\b`, regexp2.IgnoreCase)

// languageGroups is languageRegex's groups. English is Radarr's
// `lowerTitle.Contains("english")` rule, as a whole word: without it an
// English release could only ever be the default, and once an untagged
// release takes the item's original language (LanguagesFor), "ENGLISH" in a
// title is the only way left to say a release of a Japanese film is not
// Japanese.
var languageGroups = []string{"English", "French", "German", "Spanish", "Italian", "Japanese", "Korean", "Chinese"}

// detectLanguages returns every language named in title, each once and in
// languageGroups order, or nil when the title names none -- Radarr's and
// Sonarr's Language.Unknown. Radarr's LanguageParser.ParseLanguages
// (develop) builds a list the same way: it runs every language check and
// every LanguageRegex match rather than stopping at the first, then
// DistinctBy's the result, so "FRENCH.GERMAN" is both. A regexp2
// MatchTimeout ends the scan with what was found so far.
func detectLanguages(title string) []string {
	found := make(map[string]bool, len(languageGroups))
	m, err := languageRegex.FindStringMatch(title)
	for err == nil && m != nil {
		for _, name := range languageGroups {
			if grp := m.GroupByName(name); grp != nil && len(grp.Captures) > 0 {
				found[name] = true
			}
		}
		m, err = languageRegex.FindNextMatch(m)
	}
	m, err = languageCodePairRegex.FindStringMatch(title)
	for err == nil && m != nil {
		for _, code := range strings.Split(m.String(), "+") {
			found[languageCodes[strings.ToUpper(strings.TrimSpace(code))]] = true
		}
		m, err = languageCodePairRegex.FindNextMatch(m)
	}
	explicit := explicitLanguages(title)
	if len(found) == 0 && len(explicit) == 0 {
		return nil
	}
	langs := explicit
	for _, name := range languageGroups {
		if found[name] && !slices.Contains(langs, name) {
			langs = append(langs, name)
		}
	}
	return langs
}

// explicitLanguages is the languages a run of three-letter codes names
// (languageCode3Regex), in the title's order, each once.
func explicitLanguages(title string) []string {
	var out []string
	m, err := languageCode3Regex.FindStringMatch(title)
	for err == nil && m != nil {
		for _, code := range strings.FieldsFunc(m.String(), func(r rune) bool { return strings.ContainsRune(" ._+-", r) }) {
			if name, ok := languageCode3s[strings.ToUpper(code)]; ok && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		m, err = languageCode3Regex.FindNextMatch(m)
	}
	return out
}

// namesOriginal reports whether title's scene tags name the original
// language beside the languages it names (germanOriginalRegex,
// frenchOriginalRegex).
func namesOriginal(title string, langs []string) bool {
	match := func(re *regexp2.Regexp) bool { ok, err := re.MatchString(title); return err == nil && ok }
	return (slices.Contains(langs, "German") && match(germanOriginalRegex)) ||
		(slices.Contains(langs, "French") && match(frenchOriginalRegex))
}

// namedLanguages is every language title names (detectLanguages) with its
// audio markers read, nil when it names none: a dual-audio marker adds
// English and reports dual, so LanguagesFor can add the item's original
// language; a DUBBED title that names nothing else is an English dub.
// Sonarr reads neither marker.
func namedLanguages(title string) (langs []string, dual, original bool) {
	langs = detectLanguages(title)
	original = namesOriginal(title, langs)
	if ok, err := dualAudioRegex.MatchString(title); err == nil && ok {
		return withLanguage(langs, "English"), true, original
	}
	if langs == nil {
		if ok, err := dubbedRegex.MatchString(title); err == nil && ok {
			return []string{"English"}, false, false
		}
	}
	return langs, false, original
}

// withLanguage is langs with name, in languageGroups order.
func withLanguage(langs []string, name string) []string {
	if slices.Contains(langs, name) {
		return langs
	}
	out := append(slices.Clone(langs), name)
	slices.SortFunc(out, func(a, b string) int { return slices.Index(languageGroups, a) - slices.Index(languageGroups, b) })
	return out
}

// languagesOf is a movie or TV title's languages: those it names, or the
// ["English"] default with unknown set; dual is namedLanguages'.
func languagesOf(title string) (langs []string, unknown, dual, original bool) {
	if l, dual, original := namedLanguages(title); l != nil {
		return l, false, dual, original
	}
	return []string{"English"}, true, false, false
}

// parseLanguages extracts the language tags from a release title. No match
// defaults to ["English"]; languagesOf also reports whether it did.
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
// The ["English"] default is item-independent and is not what Radarr ends up
// with: its parser says Unknown, and AggregateLanguages then substitutes the
// item's original language. ParsedRelease.LanguageUnknown records that no
// language was named, and ParsedRelease.LanguagesFor applies the
// substitution for a caller that knows the item.
func parseLanguages(title string) []string {
	l, _, _, _ := languagesOf(title)
	return l
}

// LanguagesFor is p's languages for an item whose original language is
// originalLanguage (a Radarr English display name, "" when unknown). A
// release whose title names no language takes the item's original
// language, as Radarr's and Sonarr's AggregateLanguages do (develop, fetched
// 2026-09-23: Radarr "Use movie language as fallback if we couldn't parse a
// language", Sonarr "Use series language as fallback if we couldn't parse a
// language" -- languages.Count == 0 or [Unknown] becomes
// [OriginalLanguage]). With the original language unknown the parsed
// default stands, which is also where Radarr's fallback would leave a
// release of an item with no known language.
//
// A dual-audio title (DualAudio) that names no language but English is the
// original language and English, the original first. A title whose scene
// tags name the original track (NamesOriginal: German DL or ML, French VO or
// MULTi) is its named languages and the original, or English beside them
// when the original is one of them.
func (p *ParsedRelease) LanguagesFor(originalLanguage string) []string {
	switch {
	case originalLanguage == "":
		return p.Languages
	case p.LanguageUnknown:
		return []string{originalLanguage}
	case p.DualAudio && originalLanguage != "English" &&
		!slices.ContainsFunc(p.Languages, func(l string) bool { return l != "English" }):
		return []string{originalLanguage, "English"}
	case p.NamesOriginal && !slices.Contains(p.Languages, originalLanguage):
		return append(slices.Clone(p.Languages), originalLanguage)
	case p.NamesOriginal && !slices.Contains(p.Languages, "English"):
		// German DL of a German film: German and, beside it, English.
		return append(slices.Clone(p.Languages), "English")
	}
	return p.Languages
}
