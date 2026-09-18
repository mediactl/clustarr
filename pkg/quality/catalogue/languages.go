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
	// "Original", which is Radarr's pseudo-language for "whatever the
	// item was made in" and therefore has no code.
	Tag string
}

// languages maps Radarr/Sonarr's internal numeric Language.Id to its
// English display Name and ISO-639-1 code. The ids and names are
// transcribed from Radarr's src/NzbDrone.Core/Languages/Language.cs at tag
// v6.4.4.10685
// (https://raw.githubusercontent.com/Radarr/Radarr/v6.4.4.10685/src/NzbDrone.Core/Languages/Language.cs,
// fetched 2026-09-18; Sonarr's own copy of the same file is identical in
// content, only the namespace differs).
//
// The Tag column is the ISO-639-1 two-letter code for the language each
// row already identifies -- "en" for English, "fr" for French, "de" for
// German, "ja" for Japanese, "zh" for Chinese, "ko" for Korean. Language.cs
// itself carries no code, so these are derived from the language
// identities in the table rather than transcribed from it; nothing new was
// fetched to write them. They exist because three vocabularies meet here:
// pkg/release.ParsedRelease.Languages and every CondLanguage condition use
// the English Name, QualityProfileSpec.Language is a BCP-47 tag, and
// pkg/subtitles.LangKey is BCP-47 too. LanguageTag/LanguageName are the
// only sanctioned conversion between them.
//
// This table intentionally covers only the language ids that actually
// appear in a LanguageSpecification anywhere in the vendored corpus
// (testdata/trash/docs/json/{radarr,sonarr}/cf/*.json), not Language.cs's
// full ~57-entry list: -2 (Original), 1 (English), 2 (French), 4 (German),
// 8 (Japanese), 10 (Chinese), 21 (Korean). Extend this table, and re-derive
// it from the corpus scan below, before embedding any new format that
// references a LanguageSpecification id not already listed here.
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
// pkg/release's own language table is itself partial (its doc comment: "a
// bounded token table covering the languages the fixture corpus exercises,
// not the full ~50-entry Radarr language id list") and does not detect
// Chinese at all yet; id 10 is still recorded here for corpus fidelity, but
// a CondLanguage condition comparing against it cannot match today's
// pkg/release output until that table is extended -- a pkg/release
// improvement, not something this package can fix.
var languages = []Language{
	{ID: -2, Name: "Original", Tag: ""},
	{ID: 1, Name: "English", Tag: "en"},
	{ID: 2, Name: "French", Tag: "fr"},
	{ID: 4, Name: "German", Tag: "de"},
	{ID: 8, Name: "Japanese", Tag: "ja"},
	{ID: 10, Name: "Chinese", Tag: "zh"},
	{ID: 21, Name: "Korean", Tag: "ko"},
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
