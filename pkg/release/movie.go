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
// the three separator conventions the fixture corpus exercises: dot,
// underscore and space.
var movieTitleYearRegex = mustCompile(`(?<title>.+?)[.\s_](?<year>19\d{2}|20\d{2})[.\s_]`, regexp2.IgnoreCase)

// cleanTitleSeparators replaces scene-style separators with spaces and
// collapses the result, for display in ParsedRelease.Title.
func cleanTitleSeparators(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// parseMovie parses a movie release title: title, year, quality, revision,
// release group, hash, edition, hints and languages. Season/episode fields
// stay at their zero values.
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

	return &ParsedRelease{
		Title:       cleanTitleSeparators(rawTitle),
		Year:        year,
		Quality:     q,
		Revision:    rev,
		Group:       group,
		Hash:        hash,
		Edition:     edition,
		ReleaseType: commonv1.ReleaseTypeSingle,
	}, nil
}
