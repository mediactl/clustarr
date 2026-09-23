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

// idTokenRegex matches one embedded provider-id token in every spelling the
// Jellyfin/Plex/Emby/*arr folder-naming conventions use: the key "tmdb",
// "imdb" or "tvdb", optionally suffixed "id", then "-" (or Emby's "="), then
// the value, wrapped in a MATCHED pair of square brackets or braces --
// "[tmdbid-603]", "{tmdb-603}", "{tmdbid-603}", "[tmdb=603]",
// "[imdbid-tt0133093]", "{imdbid-tt0133093}", "[tvdb-81189]", ... Radarr's
// and Sonarr's own folder formats emit "{tmdb-N}"/"[tmdbid-N]" and
// "{tvdb-N}"/"[tvdbid-N]"; Jellyfin additionally accepts the braced "id"
// form, which is why an earlier table of seven fixed spellings missed
// "{tvdbid-121361}" and "{imdbid-tt0133093}".
//
// The value's shape is part of the pattern: imdb keeps its "tt" prefix and
// the others are all digits, so "{tmdb-tt1}" or "[imdb-603]" is not an id
// and is left alone rather than recorded under the wrong key.
var idTokenRegex = mustCompile(
	`\[(?<key>tmdb|tvdb)(?:id)?[-=](?<val>\d+)\]|\{(?<key>tmdb|tvdb)(?:id)?[-=](?<val>\d+)\}|`+
		`\[(?<key>imdb)(?:id)?[-=](?<val>tt\d+)\]|\{(?<key>imdb)(?:id)?[-=](?<val>tt\d+)\}`,
	regexp2.IgnoreCase,
)

// bareImdbRegex matches an imdb id carried outside idTokenRegex's keyed
// forms: a standalone "tt1234567" token, or one wrapped in its own brackets,
// braces or parentheses ("[tt1234567]", "{tt1234567}", "(tt1234567)"). The
// wrapper is part of the match so that removing the id removes its
// delimiters with it -- leaving "{}" or "[]" behind is how an earlier
// version left "{imdbid-}" in the stripped title, which then classified as
// an audiobook narrator token. A "tt" value directly after "-" or "=" is the
// value half of a keyed token idTokenRegex rejected (say "{tmdb-tt1234567}"),
// and taking the value alone out of it would leave "{tmdb-}", so it is left
// whole.
var bareImdbRegex = mustCompile(`[\[{(](?<val>tt\d{7,8})[\]})]|(?<![-=])\b(?<val>tt\d{7,8})\b`, regexp2.IgnoreCase)

// extractIDs finds every embedded provider id token in title, returning the
// ids found (nil when there are none -- see TestExtractIDsLeavesTitleWithNoIDsUnchanged)
// and title with every matched token removed, delimiters included, so the
// rest of the parsing pipeline (classification, title/year, quality,
// group) never sees any part of one. The first token for a key wins. The
// bare imdb fallback only runs once no keyed token has already supplied an
// "imdb" id, so it can't double-count the value inside one.
func extractIDs(title string) (map[string]string, string) {
	var ids map[string]string
	record := func(key, val string) {
		if ids == nil {
			ids = make(map[string]string, 3)
		}
		if _, seen := ids[key]; !seen {
			ids[key] = val
		}
	}

	cleaned := title
	for m, err := idTokenRegex.FindStringMatch(cleaned); err == nil && m != nil; m, err = idTokenRegex.FindNextMatch(m) {
		record(strings.ToLower(m.GroupByName("key").String()), strings.ToLower(m.GroupByName("val").String()))
	}
	if ids != nil {
		if replaced, err := idTokenRegex.Replace(cleaned, "", 0, -1); err == nil {
			cleaned = replaced
		}
	}

	if ids["imdb"] == "" {
		if m, err := bareImdbRegex.FindStringMatch(cleaned); err == nil && m != nil {
			record("imdb", strings.ToLower(m.GroupByName("val").String()))
			if replaced, rerr := bareImdbRegex.Replace(cleaned, "", 0, -1); rerr == nil {
				cleaned = replaced
			}
		}
	}

	return ids, cleaned
}
