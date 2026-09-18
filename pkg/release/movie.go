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

// cleanTitleSeparators replaces scene-style separators with spaces and
// collapses the result, for display in ParsedRelease.Title.
func cleanTitleSeparators(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// parseMovie parses a movie release title: title, year, quality, revision,
// release group, hash, edition, hints and languages. Season/episode fields
// stay at their zero values.
//
// extractIDs runs first, against the whole original title, and every
// subsequent step (title/year, quality, group, hints, languages, alternate
// titles) works off the id-stripped text — a folder name's
// "[tmdbid-949]"/"{imdb-tt0113277}"/etc. token is provider metadata, not
// part of the title, and left in place it could otherwise confuse the
// trailing-dash release-group pattern.
func parseMovie(title string) (*ParsedRelease, error) {
	ids, stripped := extractIDs(title)

	m, err := movieTitleYearRegex.FindStringMatch(stripped)
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

	q, rev, _, _ := parseQualityTags(stripped)
	group, hash, edition := parseGroup(stripped)

	cleanedTitle := cleanTitleSeparators(rawTitle)
	titles := buildTitles(cleanedTitle, stripped)

	return &ParsedRelease{
		Title:       titles[0],
		Titles:      titles,
		Year:        year,
		Quality:     q,
		Revision:    rev,
		Group:       group,
		Hash:        hash,
		Edition:     edition,
		ReleaseType: commonv1.ReleaseTypeSingle,
		Hints:       parseHints(stripped),
		Languages:   parseLanguages(stripped),
		IDs:         ids,
	}, nil
}
