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
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// movieTitleYearRegex captures a movie title up to its release year, across
// the three scene-style separator conventions the fixture corpus exercises
// (dot, underscore, space) and the Jellyfin/Plex "Title (Year)" convention
// (year boundaries also accept a leading "(" and trailing ")").
var movieTitleYearRegex = mustCompile(`(?<title>.+?)[.\s_(](?<year>19\d{2}|20\d{2})[.\s_)]`, regexp2.IgnoreCase)

// parentheticalYearRegex and trailingSeparatorRegex are plain stdlib regexp
// (no lookaround needed), matching normalize.go's/titles.go's helper
// regexes.
//
// parentheticalYearRegex strips an embedded "(Year)" release-year token —
// the Jellyfin/Plex "Title (Year)" convention — from a title captured by a
// pattern that, unlike movieTitleYearRegex, doesn't parse the year out on
// its own (the standard-series regexes in tv.go capture everything up to
// their own "S\d+E\d+" token regardless of what precedes it, so
// "Breaking Bad (2008)" arrives here as one span).
//
// trailingSeparatorRegex strips a dangling "-"/"_"/"." left at the end of a
// captured title: the standard-series title/separator boundary
// "[.\s_]S\d+" only ever consumes the single character directly before
// "S", so "Breaking Bad (2008) - S01E01 ..." leaves the dash in the
// captured title ("Breaking Bad (2008) -") even after the year above is
// stripped.
var (
	parentheticalYearRegex = regexp.MustCompile(`\s*\((?:19|20)\d{2}\)\s*`)
	trailingSeparatorRegex = regexp.MustCompile(`[\s._-]+$`)
)

// cleanTitleSeparators strips an embedded "(Year)" and replaces scene-style
// separators with spaces, collapsing the result and trimming any dangling
// trailing separator, for display in ParsedRelease.Title.
func cleanTitleSeparators(s string) string {
	s = parentheticalYearRegex.ReplaceAllString(s, " ")
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	s = trailingSeparatorRegex.ReplaceAllString(strings.TrimSpace(s), "")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// parseMovie parses a movie release title: title, year, quality, revision,
// release group, hash, edition, hints and languages. Season/episode fields
// stay at their zero values.
//
// title arrives already id-stripped: Parse's shared pre-dispatch step
// (parse.go) runs extractIDs before dispatching to any kind-specific
// parser, and also builds Titles/merges IDs onto the result afterward, so
// this function (like parseSeries, parseMusic, ...) neither knows nor
// cares about either concern.
func parseMovie(title string) (*ParsedRelease, error) {
	m, err := movieTitleYearRegex.FindStringMatch(title)
	if err != nil {
		return nil, fmt.Errorf("release: movie: title/year match: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("release: %q does not match the movie title/year pattern", title)
	}

	rawTitle := m.GroupByName("title").String()
	yearStr := m.GroupByName("year").String()
	year, convErr := strconv.Atoi(yearStr)
	if convErr != nil {
		return nil, fmt.Errorf("release: movie: parsing year %q: %w", yearStr, convErr)
	}

	q, rev, _, _ := parseQualityTags(title)
	group, hash, edition := parseGroup(title)

	langs, unknown := languagesOf(title)
	return &ParsedRelease{
		Languages:       langs,
		LanguageUnknown: unknown,
		Title:           cleanTitleSeparators(rawTitle),
		Year:            year,
		Quality:         q,
		Revision:        rev,
		Group:           group,
		Hash:            hash,
		Edition:         edition,
		ReleaseType:     commonv1.ReleaseTypeSingle,
		Hints:           parseHints(title),
	}, nil
}
