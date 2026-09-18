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
	"github.com/dlclark/regexp2"
)

// idSpecs are the Jellyfin/Plex/*arr folder-naming conventions for embedded
// provider ids: "[tmdbid-123]", "{tmdb-123}", "[imdbid-tt1234567]",
// "{imdb-tt1234567}", "[tvdbid-123]", "{tvdb-123}" and "[tvdb-123]". Each
// entry's capture group is the id value stored under IDs[key] — imdb keeps
// its "tt" prefix, the others don't.
var idSpecs = []struct {
	re  *regexp2.Regexp
	key string
}{
	{mustCompile(`\[tmdbid-(\d+)\]`, regexp2.IgnoreCase), "tmdb"},
	{mustCompile(`\{tmdb-(\d+)\}`, regexp2.IgnoreCase), "tmdb"},
	{mustCompile(`\[imdbid-(tt\d+)\]`, regexp2.IgnoreCase), "imdb"},
	{mustCompile(`\{imdb-(tt\d+)\}`, regexp2.IgnoreCase), "imdb"},
	{mustCompile(`\[tvdbid-(\d+)\]`, regexp2.IgnoreCase), "tvdb"},
	{mustCompile(`\{tvdb-(\d+)\}`, regexp2.IgnoreCase), "tvdb"},
	{mustCompile(`\[tvdb-(\d+)\]`, regexp2.IgnoreCase), "tvdb"},
}

// bareImdbRegex matches a standalone "tt1234567" token (no brackets), the
// shape a title carries the imdb id in when it isn't already wrapped in one
// of idSpecs' bracket/brace forms.
var bareImdbRegex = mustCompile(`\b(tt\d{7,8})\b`, regexp2.IgnoreCase)

// extractIDs finds every embedded provider id token in title, returning the
// ids found (never nil unless empty — see TestExtractIDsLeavesTitleWithNoIDsUnchanged)
// and title with every matched token removed, so the rest of the parsing
// pipeline (title/year, quality, group) never sees them. The bare imdb
// fallback only runs once none of the bracketed/braced forms have already
// claimed an "imdb" id, so it can't double-match the "tt1234567" inside an
// already-stripped "[imdbid-tt1234567]" token.
func extractIDs(title string) (map[string]string, string) {
	var ids map[string]string
	cleaned := title

	for _, spec := range idSpecs {
		m, err := spec.re.FindStringMatch(cleaned)
		if err != nil || m == nil {
			continue
		}
		g := m.GroupByNumber(1)
		if g == nil || len(g.Captures) == 0 {
			continue
		}
		if ids == nil {
			ids = make(map[string]string, 3)
		}
		ids[spec.key] = g.String()
		if replaced, rerr := spec.re.Replace(cleaned, "", 0, -1); rerr == nil {
			cleaned = replaced
		}
	}

	if ids == nil || ids["imdb"] == "" {
		if m, err := bareImdbRegex.FindStringMatch(cleaned); err == nil && m != nil {
			if ids == nil {
				ids = make(map[string]string, 1)
			}
			ids["imdb"] = m.String()
			if replaced, rerr := bareImdbRegex.Replace(cleaned, "", 0, -1); rerr == nil {
				cleaned = replaced
			}
		}
	}

	return ids, cleaned
}
