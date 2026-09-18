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

package catalogue

import "strings"

// Language is one row of the language table: Radarr's internal numeric
// Language.Id, its English display Name (the vocabulary the embedded
// custom formats and pkg/release both speak) and its ISO-639-1 Tag (the
// vocabulary QualityProfileSpec.Language and pkg/subtitles speak).
type Language struct {
	// ID is Radarr's Language.Id.
	ID int32
	// Name is Radarr's English Language.Name ("English", "Japanese").
	Name string
	// Tag is the ISO-639-1 two-letter code ("en", "ja"). It is empty for
	// the rows that have no such code: Radarr's three pseudo-languages
	// (Original, Any, Unknown), Flemish, and the two regional variants
	// Portuguese (Brazil) and Spanish (Latino) -- see the table below.
	Tag string
}

// languages is Radarr/Sonarr's full Language.cs set: the internal numeric
// Language.Id, its English display Name, and its ISO-639-1 Tag. Ids and
// names are transcribed from Radarr's
// src/NzbDrone.Core/Languages/Language.cs at tag v6.4.4.10685
// (https://raw.githubusercontent.com/Radarr/Radarr/v6.4.4.10685/src/NzbDrone.Core/Languages/Language.cs,
// first fetched 2026-09-18 and re-fetched at that same tag when this table
// was widened; Sonarr's own copy of the file is identical in content, only
// the namespace differs). All 60 entries, ids -2 through 57, in id order.
//
// The table serves two purposes, which is why it carries the whole set and
// not only the ids the vendored corpus uses:
//
//  1. Custom-format matching. A CondLanguage Condition holds an English
//     Name, because that is the vocabulary
//     pkg/release.ParsedRelease.Languages is built from (see
//     pkg/release/language.go's languageGroups and its "no match defaults
//     to English" rule). LanguageByID translates the corpus's raw numeric
//     LanguageSpecification values into it, which is how parity_test.go
//     checks the embedded formats for completeness.
//  2. Profile resolution. QualityProfileSpec.Language is "original", "any"
//     or a BCP-47 tag, validated only as MaxLength=64, and quality.FromCRD
//     resolves it through LanguageName into the same Name vocabulary. A
//     table covering only the corpus ids would turn a perfectly reasonable
//     "es" or "hi" into an Invalid profile: narrowing this table narrows
//     the CRD.
//
// The Tag column is the ISO-639-1 two-letter code for the language each row
// identifies. Language.cs carries no code of its own, so these are derived
// from the language identities already in the table; nothing beyond
// Language.cs was fetched to write them. Six rows carry no tag: the three
// pseudo-languages (Original, Any, Unknown), Flemish (a variety of Dutch --
// BCP-47 nl-BE, with no ISO-639-1 code of its own; giving it "nl" would
// make the round trip ambiguous with Dutch), and the two regional variants
// Portuguese (Brazil) and Spanish (Latino), which are BCP-47 regions:
// "pt-BR" and "es-419" resolve through their primary subtag to Portuguese
// and Spanish.
//
// The ids the vendored corpus
// (testdata/trash/docs/json/{radarr,sonarr}/cf/*.json) actually references
// in a LanguageSpecification are -2, 1, 2, 4, 8, 10 and 21; re-derive that
// list with:
//
//	python3 -c "
//	import json, glob
//	ids = set()
//	for f in glob.glob('testdata/trash/docs/json/*/cf/*.json'):
//	    for s in json.load(open(f)).get('specifications', []):
//	        if s.get('implementation') == 'LanguageSpecification':
//	            ids.add(s['fields']['value'])
//	print(sorted(ids))"
//
// Note pkg/release's own language table is partial (its doc comment: "a
// bounded token table covering the languages the fixture corpus exercises,
// not the full ~50-entry Radarr language id list") and does not detect
// Chinese at all yet, so a CondLanguage condition -- or a profile language
// -- naming a language it cannot parse out of a release title matches
// nothing today. That is a pkg/release improvement, not something this
// package can fix.
var languages = []Language{
	{ID: -2, Name: "Original", Tag: ""}, // the three pseudo-languages: not languages, so no ISO-639-1 code
	{ID: -1, Name: "Any", Tag: ""},
	{ID: 0, Name: "Unknown", Tag: ""},
	{ID: 1, Name: "English", Tag: "en"},
	{ID: 2, Name: "French", Tag: "fr"},
	{ID: 3, Name: "Spanish", Tag: "es"},
	{ID: 4, Name: "German", Tag: "de"},
	{ID: 5, Name: "Italian", Tag: "it"},
	{ID: 6, Name: "Danish", Tag: "da"},
	{ID: 7, Name: "Dutch", Tag: "nl"},
	{ID: 8, Name: "Japanese", Tag: "ja"},
	{ID: 9, Name: "Icelandic", Tag: "is"},
	{ID: 10, Name: "Chinese", Tag: "zh"},
	{ID: 11, Name: "Russian", Tag: "ru"},
	{ID: 12, Name: "Polish", Tag: "pl"},
	{ID: 13, Name: "Vietnamese", Tag: "vi"},
	{ID: 14, Name: "Swedish", Tag: "sv"},
	{ID: 15, Name: "Norwegian", Tag: "no"},
	{ID: 16, Name: "Finnish", Tag: "fi"},
	{ID: 17, Name: "Turkish", Tag: "tr"},
	{ID: 18, Name: "Portuguese", Tag: "pt"},
	{ID: 19, Name: "Flemish", Tag: ""}, // nl-BE; ISO-639-1 has no code of its own for it
	{ID: 20, Name: "Greek", Tag: "el"},
	{ID: 21, Name: "Korean", Tag: "ko"},
	{ID: 22, Name: "Hungarian", Tag: "hu"},
	{ID: 23, Name: "Hebrew", Tag: "he"},
	{ID: 24, Name: "Lithuanian", Tag: "lt"},
	{ID: 25, Name: "Czech", Tag: "cs"},
	{ID: 26, Name: "Hindi", Tag: "hi"},
	{ID: 27, Name: "Romanian", Tag: "ro"},
	{ID: 28, Name: "Thai", Tag: "th"},
	{ID: 29, Name: "Bulgarian", Tag: "bg"},
	{ID: 30, Name: "Portuguese (Brazil)", Tag: ""}, // pt-BR: a BCP-47 region, not a language
	{ID: 31, Name: "Arabic", Tag: "ar"},
	{ID: 32, Name: "Ukrainian", Tag: "uk"},
	{ID: 33, Name: "Persian", Tag: "fa"},
	{ID: 34, Name: "Bengali", Tag: "bn"},
	{ID: 35, Name: "Slovak", Tag: "sk"},
	{ID: 36, Name: "Latvian", Tag: "lv"},
	{ID: 37, Name: "Spanish (Latino)", Tag: ""}, // es-419: likewise
	{ID: 38, Name: "Catalan", Tag: "ca"},
	{ID: 39, Name: "Croatian", Tag: "hr"},
	{ID: 40, Name: "Serbian", Tag: "sr"},
	{ID: 41, Name: "Bosnian", Tag: "bs"},
	{ID: 42, Name: "Estonian", Tag: "et"},
	{ID: 43, Name: "Tamil", Tag: "ta"},
	{ID: 44, Name: "Indonesian", Tag: "id"},
	{ID: 45, Name: "Telugu", Tag: "te"},
	{ID: 46, Name: "Macedonian", Tag: "mk"},
	{ID: 47, Name: "Slovenian", Tag: "sl"},
	{ID: 48, Name: "Malayalam", Tag: "ml"},
	{ID: 49, Name: "Kannada", Tag: "kn"},
	{ID: 50, Name: "Albanian", Tag: "sq"},
	{ID: 51, Name: "Afrikaans", Tag: "af"},
	{ID: 52, Name: "Marathi", Tag: "mr"},
	{ID: 53, Name: "Tagalog", Tag: "tl"},
	{ID: 54, Name: "Urdu", Tag: "ur"},
	{ID: 55, Name: "Romansh", Tag: "rm"},
	{ID: 56, Name: "Mongolian", Tag: "mn"},
	{ID: 57, Name: "Georgian", Tag: "ka"},
}

// Languages returns a copy of the language table, in id order.
func Languages() []Language {
	out := make([]Language, len(languages))
	copy(out, languages)
	return out
}

// LanguageByID looks up a Radarr language id's English name -- exported so
// parity_test.go (in the external catalogue_test package) can translate the
// vendored corpus's raw numeric LanguageSpecification values into the same
// string vocabulary this package's embedded data/formats/*.json conditions
// use, to check them for completeness against the corpus.
func LanguageByID(id int32) (string, bool) {
	for _, l := range languages {
		if l.ID == id {
			return l.Name, true
		}
	}
	return "", false
}

// LanguageTag converts a Radarr English language name ("Japanese") to its
// ISO-639-1 code ("ja"), case-insensitively. It reports false for a name
// the table does not carry and for "Original", which has no code.
func LanguageTag(name string) (string, bool) {
	for _, l := range languages {
		if l.Tag != "" && strings.EqualFold(l.Name, name) {
			return l.Tag, true
		}
	}
	return "", false
}

// LanguageName converts a BCP-47 tag to the Radarr English language name
// the catalogue's conditions and pkg/release.ParsedRelease.Languages use.
// It matches on the tag's primary subtag, case-insensitively, so "en",
// "EN", "en-US" and "en-Latn-US" all resolve to "English". It reports
// false for a tag whose language the table does not carry.
func LanguageName(tag string) (string, bool) {
	primary, _, _ := strings.Cut(tag, "-")
	if primary == "" {
		return "", false
	}
	for _, l := range languages {
		if l.Tag != "" && strings.EqualFold(l.Tag, primary) {
			return l.Name, true
		}
	}
	return "", false
}
