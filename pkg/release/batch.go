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
	"fmt"
	"regexp"
	"strings"
)

var (
	// batchLeadRegex is the bracketed groups a title opens with, and
	// batchTailRegex the bracketed and parenthesised tags it closes with.
	batchLeadRegex = regexp.MustCompile(`^(?:\s*\[[^\]]*\][\s._-]*)+`)
	batchTailRegex = regexp.MustCompile(`(?:[\s._-]*(?:\[[^\]]*\]|\([^)]*\)))+\s*$`)
	// batchYearRegex is a year standing in a title, bare or in parentheses.
	batchYearRegex = regexp.MustCompile(`[（(]?\b(?:19|20)\d{2}\b[)）]?`)
)

// SeriesBatchTitle reads a title that names a series and nothing else --
// no season, no episode, only the series between a release group and its
// tags -- as the pack of that series' one season, and returns it written
// the way a season pack is, "<groups> <the series' title> S<season>
// <tags>", for Parse to read. "" when the title is not of that shape or
// what it names is not one of titles.
//
// A fansub batch of a one-season anime is often named so, "[GERMini] Rozen
// Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]", and no
// release pattern takes a title with no number in it: on talos thirteen
// episodes stayed missing with that batch in every search (2026-11-27).
// The title alone cannot say it is a whole series, which is why the caller
// passes the titles of a series it knows has one season, and why the match
// is exact: what stands between the groups and the tags, less a year, must
// be one of titles less a year, compared as CleanTitle -- and once more
// with the letters outside ASCII dropped instead of folded, as a usenet
// post writes "Zurckspulen". A title that carries anything else there (a
// quality word outside brackets, "Movie", "OVA", a part) matches none.
func SeriesBatchTitle(title string, titles []string, season int) string {
	if season < 1 {
		return ""
	}
	lead := batchLeadRegex.FindString(title)
	rest := title[len(lead):]
	tail := batchTailRegex.FindString(rest)
	core := rest[:len(rest)-len(tail)]
	key := batchKey(core)
	if len(key) < 3 {
		return ""
	}
	for _, t := range titles {
		if key != batchKey(t) && key != batchKey(dropNonASCII(t)) {
			continue
		}
		written := strings.TrimSpace(batchYearRegex.ReplaceAllString(t, " "))
		if written == "" {
			continue
		}
		return strings.TrimSpace(fmt.Sprintf("%s %s S%02d %s",
			strings.TrimSpace(lead), written, season, strings.TrimLeft(tail, " ._-")))
	}
	return ""
}

// batchKey is a title as SeriesBatchTitle compares it: separators read as
// spaces, a year dropped, cleaned.
func batchKey(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	return CleanTitle(batchYearRegex.ReplaceAllString(s, " "))
}

// dropNonASCII is s without its runes outside ASCII.
func dropNonASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r > 127 {
			return -1
		}
		return r
	}, s)
}
