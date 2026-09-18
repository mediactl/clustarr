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
	"time"

	"github.com/dlclark/regexp2"
)

// dailyRegex matches a talk-show/news-style airdate title, e.g.
// "The.Daily.Show.2018.10.12.Trevor.Noah...".
var dailyRegex = mustCompile(
	`(?<title>.+?)[.\s_](?<year>19\d{2}|20\d{2})[.\s_](?<month>\d{2})[.\s_](?<day>\d{2})[.\s_]`,
	regexp2.IgnoreCase,
)

// parseDailySeries parses a daily/talk-show release title, extracting its
// air date instead of a season/episode pair.
func parseDailySeries(title string) (*ParsedRelease, error) {
	m, err := dailyRegex.FindStringMatch(title)
	if err != nil {
		return nil, fmt.Errorf("release: daily: match: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("release: %q does not match the daily air-date pattern", title)
	}

	year, err := atoiGroup(m, "year")
	if err != nil {
		return nil, err
	}
	month, err := atoiGroup(m, "month")
	if err != nil {
		return nil, err
	}
	day, err := atoiGroup(m, "day")
	if err != nil {
		return nil, err
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return nil, fmt.Errorf("release: %q has an out-of-range air date %04d-%02d-%02d", title, year, month, day)
	}

	airDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	p := &ParsedRelease{
		Title:       cleanTitleSeparators(m.GroupByName("title").String()),
		AirDate:     &airDate,
		ReleaseType: releaseTypeForEpisodes(nil),
	}
	return finishSeries(title, p), nil
}
