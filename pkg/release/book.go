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

// bookRegex matches "Author - Title (Year|Unabridged) [Format]". This is
// the shape moistari/rls fails on entirely for the audiobook case
// (docs/research/quality.md §7.3: "type unknown, title unparsed").
var bookRegex = mustCompile(
	`(?<author>.+?)\s-\s(?<title>.+?)\s\((?<yearOrUnabridged>\d{4}|Unabridged)\)\s\[(?<fmt>[A-Za-z0-9 ]+)\]`,
	regexp2.IgnoreCase,
)

// audiobookNarratorRegex matches "Title - Author {Narrator} [ASIN xxx] [Format]".
var audiobookNarratorRegex = mustCompile(
	`(?<title>.+?)\s-\s(?<author>.+?)\s\{(?<narrator>[^}]+)\}\s\[ASIN\s(?<asin>[A-Z0-9]{10})\]\s\[(?<fmt>[A-Za-z0-9]+)\]`,
	regexp2.IgnoreCase,
)

// canonicalBookFormat upper-cases a format token. classify.go's
// ebookFormatRegex documents the recognized ebook set for ClassifyKind's
// purposes; here, an unrecognized token still passes through upper-cased
// rather than being dropped, since a title with a format that set doesn't
// yet list is more useful with *something* in ParsedRelease.Book.Format
// than with nothing.
func canonicalBookFormat(raw string) string {
	return strings.ToUpper(raw)
}

// parseBook parses a book or audiobook release title. It takes no MediaKind:
// Options.Kind's job — pinning the kind and skipping ClassifyKind — is
// already done by the time Parse's dispatch (parse.go) routes here, and
// common.ReleaseType has no separate audiobook value to select between
// (both shapes report commonv1.ReleaseTypeBook). Which of BookInfo's two
// shapes applies is determined entirely by which regex the title's own text
// matches (the "{Narrator} [ASIN ...]" shape vs. the plain
// "(Year|Unabridged) [Format]" shape) — matching parseMusic and parseComic,
// neither of which takes a kind parameter either. The one kind-dependent
// fact, which unknown quality an unrecognised format gets, is Parse's.
func parseBook(title string) (*ParsedRelease, error) {
	if m, err := audiobookNarratorRegex.FindStringMatch(title); err != nil {
		return nil, fmt.Errorf("release: book: narrator match: %w", err)
	} else if m != nil {
		fmtToken := strings.Fields(m.GroupByName("fmt").String())
		format := ""
		if len(fmtToken) > 0 {
			format = canonicalBookFormat(fmtToken[0])
		}
		info := &BookInfo{
			Author:   strings.TrimSpace(m.GroupByName("author").String()),
			Title:    strings.TrimSpace(m.GroupByName("title").String()),
			Format:   format,
			Narrator: strings.TrimSpace(m.GroupByName("narrator").String()),
			ASIN:     m.GroupByName("asin").String(),
		}
		q, _ := bookQuality(title)
		return &ParsedRelease{
			Title:       info.Title,
			Quality:     q,
			Revision:    revisionOrDefault(title),
			Book:        info,
			ReleaseType: commonv1.ReleaseTypeBook,
		}, nil
	}

	m, err := bookRegex.FindStringMatch(title)
	if err != nil {
		return nil, fmt.Errorf("release: book: match: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("release: %q does not match any book title pattern", title)
	}

	yearOrUnabridged := m.GroupByName("yearOrUnabridged").String()
	info := &BookInfo{
		Author: strings.TrimSpace(m.GroupByName("author").String()),
		Title:  strings.TrimSpace(m.GroupByName("title").String()),
	}
	if strings.EqualFold(yearOrUnabridged, "Unabridged") {
		info.Unabridged = true
	} else if year, convErr := strconv.Atoi(yearOrUnabridged); convErr == nil {
		info.Year = year
	}

	fmtTokens := strings.Fields(m.GroupByName("fmt").String())
	if len(fmtTokens) > 0 {
		info.Format = canonicalBookFormat(fmtTokens[0])
	}

	q, _ := bookQuality(title)
	return &ParsedRelease{
		Title:       info.Title,
		Year:        info.Year,
		Quality:     q,
		Revision:    revisionOrDefault(title),
		Book:        info,
		ReleaseType: commonv1.ReleaseTypeBook,
	}, nil
}
