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

package relindex

import (
	"strings"
	"unicode"
)

// matchExpr turns arbitrary, attacker-controlled text into an FTS5 MATCH
// expression that can only ever mean "every one of these terms appears".
//
// It does not parse the input as a query. Each whitespace-separated token is
// wrapped in double quotes, which makes it an FTS5 *string*: the tokenizer
// splits it into terms and every operator character inside it -- * : ^ - ( )
// and the bare keywords AND, OR, NOT and NEAR -- is a literal. An interior
// double quote is escaped by doubling it, which is FTS5's only escape.
//
// Tokens carrying no letter or digit are dropped. `"*"` or `"("` would quote
// cleanly but tokenise to nothing, and FTS5 rejects a phrase with no terms
// with a syntax error -- so a user typing a lone asterisk would get a 500
// rather than a result set.
//
// The return value is always passed as a BOUND parameter. This function
// defends the FTS5 grammar; parameter binding defends SQL. Neither substitutes
// for the other.
//
// It returns "" when text carries no searchable term at all, and Search then
// omits the MATCH clause entirely rather than passing an empty expression,
// which FTS5 also rejects.
//
// It deliberately does NOT normalise. ADR-0003 puts title normalisation in
// front of the store, so the caller must run Query.Text through the same
// function it ran Release.TitleNorm through, or nothing will match.
func matchExpr(text string) string {
	fields := strings.Fields(splitControls(text))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if !hasAlnum(f) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " AND ")
}

// splitControls replaces every control rune with a space, so control runes
// separate terms exactly as the unicode61 tokenizer already treats them.
//
// The brief's matchExpr did not do this and a NUL byte in Query.Text reached
// the bound parameter, where the driver hands SQLite a C string that stops at
// the NUL: the statement arrives with an unterminated quote and the search
// fails with `SQL logic error: unterminated string (1)`. Since Query.Text is
// attacker-controlled, that is a one-byte denial of service on every search.
// Dropping the rune would silently weld two terms together ("dune\x00matrix"
// becoming the single term "dunematrix"), so it is a separator instead, which
// is what the tokenizer would have done with it anyway.
//
// strings.Fields already handles \t, \n, \v, \f and \r; this covers the rest
// of the C0 and C1 ranges, including NUL and DEL.
func splitControls(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// hasAlnum reports whether s contains at least one letter or digit, i.e.
// whether the unicode61 tokenizer will get at least one term out of it.
func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
