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
	"regexp"
	"strings"

	"github.com/moistari/rls"
)

// Normalize returns title with accents stripped, curly quotes/dashes folded
// and whitespace collapsed, case preserved. It is for display / re-embedding
// (e.g. History), not for equality comparison — use CleanTitle for that.
func Normalize(title string) string {
	return rls.MustClean(title)
}

// These four helper regexes are plain stdlib regexp, not regexp2: none of
// them need lookaround, and CLAUDE.md's regexp2 mandate is specifically for
// ported TRaSH/*arr patterns that do.
//
// trailingArticleRegex looks for a trailing article after whitespace, not
// after a comma: it runs on rls.MustNormalize's output, which has already
// stripped punctuation (including the comma in "Matrix, The"), so a
// comma-anchored pattern would never match at this stage.
var (
	trailingArticleRegex = regexp.MustCompile(`(?i)\s(the|a|an)$`)
	leadingArticleRegex  = regexp.MustCompile(`(?i)^(the|a|an)\s+`)
	nonAlnumRegex        = regexp.MustCompile(`[^a-z0-9 ]+`)
	collapseSpaceRegex   = regexp.MustCompile(`\s+`)
)

// CleanTitle returns a lower-cased, article-stripped, punctuation-stripped
// form of title, stable across "The Matrix", "THE MATRIX", "Matrix, The"
// and "The  Matrix!" alike. It is the key pkg/decision and the metadata
// gateway match parsed titles against inventory titles with.
func CleanTitle(title string) string {
	s := rls.MustNormalize(title) // lower-cased, accent-stripped, punctuation-collapsed
	s = trailingArticleRegex.ReplaceAllString(s, "")
	s = leadingArticleRegex.ReplaceAllString(s, "")
	s = nonAlnumRegex.ReplaceAllString(s, "")
	s = collapseSpaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
