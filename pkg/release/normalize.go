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
	"unicode"
	"unicode/utf8"

	"github.com/moistari/rls"
	"golang.org/x/text/unicode/norm"
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
	collapseSpaceRegex   = regexp.MustCompile(`\s+`)
)

// CleanTitle returns a lower-cased, article-stripped, punctuation-stripped
// form of title, stable across "The Matrix", "THE MATRIX", "Matrix, The"
// and "The  Matrix!" alike. It is the key pkg/decision and the metadata
// gateway match parsed titles against inventory titles with.
//
// It keeps only ASCII letters and digits: a letter outside ASCII that
// accent-stripping cannot fold ("Ø", "Матрица", "日本語") is dropped, so a
// wholly non-Latin title cleans to "". pkg/decision's identity check is
// built around exactly that (it fails such a title closed), which is why
// the Unicode-aware form is a separate function, TitleNorm, rather than a
// change to this one. The two share every step but the last, so
// CleanTitle(s) is always TitleNorm(s) with its non-ASCII runes removed.
func CleanTitle(title string) string {
	return cleanTitle(title, isASCIIAlnum)
}

// TitleNorm is the normaliser for pkg/relindex's full-text index: the
// function both Release.TitleNorm and Query.Text must go through, since
// relindex stores and searches what it is given. It is CleanTitle with one
// difference -- letters, digits and marks in every script survive -- so a
// release named only "Матрица", "日本語のタイトル" or "마마마" indexes
// under its own title instead of being refused as empty, and a query for
// one matches it instead of degrading to its ASCII residue ("日本語のタイトル
// 2026" to "2026", which matches every release that year).
//
// On printable ASCII the two are identical, so rows indexed through
// CleanTitle stay findable through TitleNorm, and a Latin title keeps
// CleanTitle's exact tokens: accents folded ("amélie" to "amelie"), articles
// stripped, intra-word punctuation welded ("spider-man" to "spiderman").
//
// Limitations, both symmetric because both sides share the function: the
// FTS5 unicode61 tokenizer splits on spaces and punctuation, not on word
// boundaries in scripts written without spaces, so "日本語のタイトル" is one
// term and a query for "タイトル" alone does not match it; and diacritic
// folding is applied in every script (rls drops all non-spacing marks), so
// "ガンダム" and "カンタム" share a key, much as remove_diacritics makes
// "amélie" and "amelie" share one.
func TitleNorm(title string) string {
	return cleanTitle(title, isLetterDigitOrMark)
}

// cleanTitle is the pipeline CleanTitle and TitleNorm share. keep decides
// which runes survive the final filter; every step before it is common.
//
//  1. separateRunes turns control characters, Unicode whitespace and bytes
//     that are not valid UTF-8 into spaces and drops invisible format
//     characters. Without it a control rune was deleted outright, welding its
//     neighbours ("dune\x00matrix" became the single term "dunematrix"), and
//     an invalid byte or a literal U+FFFD made rls's transformer stop, so
//     "Movie \uFFFD 2020" cleaned to "movie" -- everything after it lost.
//  2. NFKC folds compatibility forms: full-width "ＭＡＴＲＩＸ" and
//     "１０８０ｐ", ligatures, circled and superscript digits.
//  3. rls.MustNormalize lower-cases, strips accents and collapses
//     punctuation.
//  4. A leading or trailing English article is stripped.
//  5. keep filters the runes; whitespace collapses.
func cleanTitle(title string, keep func(rune) bool) string {
	s := rls.MustNormalize(norm.NFKC.String(separateRunes(title)))
	s = trailingArticleRegex.ReplaceAllString(s, "")
	s = leadingArticleRegex.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == ' ' || keep(r) {
			return r
		}
		return -1
	}, s)
	s = collapseSpaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// separateRunes is cleanTitle's step 1.
func separateRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s { // an invalid byte ranges as utf8.RuneError
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.IsSpace(r):
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r): // soft hyphen, zero-width joiners, BOM
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func isLetterDigitOrMark(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}
