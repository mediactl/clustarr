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

// languageByID maps Radarr/Sonarr's internal numeric Language.Id to its
// English display Name, transcribed from Radarr's
// src/NzbDrone.Core/Languages/Language.cs at tag v6.4.4.10685
// (https://raw.githubusercontent.com/Radarr/Radarr/v6.4.4.10685/src/NzbDrone.Core/Languages/Language.cs,
// fetched 2026-09-18; Sonarr's own copy of the same file is identical in
// content, only the namespace differs).
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
// The map's values are Language.cs's English Name field, deliberately not
// an ISO-639-1/2 code: neither Language.cs itself nor anywhere else in this
// codebase's Languages representation carries an ISO code, so inventing one
// here would be a value that could never be compared against anything real.
// pkg/release.ParsedRelease.Languages (populated by pkg/release/language.go's
// parseLanguages) is itself built from this exact English-name vocabulary
// ("English", "French", "German", "Japanese", "Korean", "Original", ...,
// see that file's languageGroups and its "no match defaults to English"
// rule) -- so a CondLanguage Condition's Value must use the same vocabulary
// to ever actually match a real parsed release. Note pkg/release's own
// language table is itself partial (its doc comment: "a bounded token table
// covering the languages the fixture corpus exercises, not the full ~50-
// entry Radarr language id list") and does not detect Chinese at all yet;
// languageByID[10] = "Chinese" is still recorded here for corpus fidelity,
// but a CondLanguage condition comparing against it cannot match today's
// pkg/release output until that table is extended -- a pkg/release
// improvement, not something this package can fix.
var languageByID = map[int32]string{
	-2: "Original",
	1:  "English",
	2:  "French",
	4:  "German",
	8:  "Japanese",
	10: "Chinese",
	21: "Korean",
}

// LanguageByID looks up languageByID -- exported so parity_test.go (in the
// external catalogue_test package) can translate the vendored corpus's raw
// numeric LanguageSpecification values into the same string vocabulary this
// package's embedded data/formats/*.json conditions use, to check them for
// completeness against the corpus.
func LanguageByID(id int32) (string, bool) {
	name, ok := languageByID[id]
	return name, ok
}
