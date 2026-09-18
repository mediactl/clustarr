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

// comicIssueRegex matches a Western-style comic release, "Series 001 (Year)".
var comicIssueRegex = mustCompile(`(?<series>.+?)\s(?<issue>\d{2,4})\s\((?<year>\d{4})\)`, regexp2.IgnoreCase)

// mangaVolChapterRegex matches a manga volume+chapter release,
// "Series v107 c1088 (Year)". Tried before comicIssueRegex since its "v\d+"
// token would otherwise also satisfy comicIssueRegex's looser series/issue
// split.
var mangaVolChapterRegex = mustCompile(
	`(?<series>.+?)\sv(?<volume>\d{1,4})\sc(?<chapter>\d{1,4})\s\((?<year>\d{4})\)`,
	regexp2.IgnoreCase,
)

// comicExtensions maps a recognized file extension to its canonical
// ComicInfo.Format spelling.
var comicExtensions = map[string]string{
	".cbz": "CBZ",
	".cbr": "CBR",
	".pdf": "PDF",
}

// splitComicExtension strips a recognized comic extension from title,
// returning the base title and the canonical format (empty when the title
// carries no recognized extension).
func splitComicExtension(title string) (base, format string) {
	lower := strings.ToLower(title)
	for ext, canonical := range comicExtensions {
		if strings.HasSuffix(lower, ext) {
			return title[:len(title)-len(ext)], canonical
		}
	}
	return title, ""
}

// parseComic parses a comic or manga release title.
func parseComic(title string) (*ParsedRelease, error) {
	base, format := splitComicExtension(title)

	if m, err := mangaVolChapterRegex.FindStringMatch(base); err != nil {
		return nil, fmt.Errorf("release: comic: manga match: %w", err)
	} else if m != nil {
		series := strings.TrimSpace(m.GroupByName("series").String())
		volume, convErr := strconv.Atoi(m.GroupByName("volume").String())
		if convErr != nil {
			return nil, fmt.Errorf("release: comic: parsing volume: %w", convErr)
		}
		year, convErr := strconv.Atoi(m.GroupByName("year").String())
		if convErr != nil {
			return nil, fmt.Errorf("release: comic: parsing year: %w", convErr)
		}
		info := &ComicInfo{
			Series: series,
			Issue:  m.GroupByName("chapter").String(),
			Volume: volume,
			Year:   year,
			Format: format,
			Manga:  true,
		}
		return &ParsedRelease{
			Title:       series,
			Year:        year,
			Comic:       info,
			ReleaseType: commonv1.ReleaseTypeIssue,
		}, nil
	}

	m, err := comicIssueRegex.FindStringMatch(base)
	if err != nil {
		return nil, fmt.Errorf("release: comic: issue match: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("release: %q does not match any comic title pattern", title)
	}

	series := strings.TrimSpace(m.GroupByName("series").String())
	year, err := atoiGroup(m, "year")
	if err != nil {
		return nil, err
	}
	info := &ComicInfo{
		Series: series,
		Issue:  m.GroupByName("issue").String(),
		Year:   year,
		Format: format,
	}
	return &ParsedRelease{
		Title:       series,
		Year:        year,
		Comic:       info,
		ReleaseType: commonv1.ReleaseTypeIssue,
	}, nil
}
