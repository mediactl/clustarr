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
	"testing"

	"github.com/stretchr/testify/require"
)

// Query.Text is attacker-controlled. It arrives from third-party indexer
// titles (via the RSS matcher's re-query path) and from user input in the UI.
// There are TWO distinct injection surfaces and they need two distinct
// defences:
//
//  1. SQL. Solved by binding: the MATCH argument is always a `?` parameter,
//     never concatenated into the statement.
//  2. The FTS5 query mini-language, which lives INSIDE that bound string and
//     has its own grammar: " for phrases, * for prefixes, : for column
//     filters, ^ for anchors, - and NOT and AND and OR and NEAR for
//     operators, and parentheses. A bound parameter does nothing about this
//     one. Unescaped, the best case is `fts5: syntax error near "..."` on
//     every search with an apostrophe in it; the worse case is a query that
//     silently means something else.
//
// The defence is to stop parsing user text as a query at all: split on
// whitespace, wrap each token in double quotes (doubling any interior quote),
// and join with AND. Inside a double-quoted FTS5 string every character is a
// literal to be tokenised -- operators, punctuation and all.
func TestMatchExprQuotesEveryTermAndNeutralisesTheGrammar(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain words", "the matrix", `"the" AND "matrix"`},
		{"collapses runs of whitespace", "  the \t matrix \n ", `"the" AND "matrix"`},
		{"empty", "", ``},
		{"whitespace only", "   \t\n ", ``},
		{"a bare quote is dropped", `"`, ``},
		{"an embedded quote is doubled", `he said "hi"`, `"he" AND "said" AND """hi"""`},
		{"a lone star is dropped", "*", ``},
		{"a trailing star becomes a literal", "matrix*", `"matrix*"`},
		{"OR is a term, not an operator", "matrix OR dune", `"matrix" AND "OR" AND "dune"`},
		{"NEAR is a term, not an operator", "matrix NEAR dune", `"matrix" AND "NEAR" AND "dune"`},
		{"NOT is a term, not an operator", "matrix NOT dune", `"matrix" AND "NOT" AND "dune"`},
		{"a leading dash is a literal", "-dune", `"-dune"`},
		{"a column filter becomes a phrase", "title_norm:foo", `"title_norm:foo"`},
		{"parentheses alone are dropped", "( )", ``},
		{"an anchor is a literal", "^matrix", `"^matrix"`},
		{"punctuation-only tokens are dropped", `matrix -- ( ) * " dune`, `"matrix" AND "dune"`},
		{"a hyphenated title survives as a phrase", "spider-man", `"spider-man"`},
		{"unicode is preserved", "amélie", `"amélie"`},
		{"digits are searchable terms", "1999", `"1999"`},
		// Not in the brief's table. A NUL reaching the bound parameter
		// truncates the C string SQLite is handed and the statement
		// arrives with an unterminated quote; control runes separate
		// terms here exactly as the unicode61 tokenizer treats them.
		{"a NUL separates terms", "dune\x00matrix", `"dune" AND "matrix"`},
		{"a lone NUL is dropped", "\x00", ``},
		{"a DEL separates terms", "dune\x7fmatrix", `"dune" AND "matrix"`},
		// The brief's table gave the first term as `"a""`, which is an
		// UNTERMINATED FTS5 string -- open quote, `a`, an escaped quote,
		// and no close. SQLite rejects it with a syntax error. The token
		// is `a"`, so the escape is `a""` and the wrapped form is `"a"""`.
		{"a quote-injection attempt is inert", `a" OR title_norm:"b`, `"a""" AND "OR" AND "title_norm:""b"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchExpr(tc.in))
		})
	}
}
