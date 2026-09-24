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

// This file lives in package cardigann (not cardigann_test, unlike every
// other _test.go file here) so it can call the unexported render function
// directly, per the brief's Step 18: the alternative was exporting a
// RenderForTest wrapper solely for tests, which is more surface for less
// benefit than one internal test file.
package cardigann

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSupportsIfRangeJoinAndReReplace(t *testing.T) {
	tc := &TemplateContext{
		Categories: []string{"42", "54"},
		Query:      QueryVars{Q: "some.movie.2024"},
		True:       "true", False: "",
		Now: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
	}
	tc.Today.Year = tc.Now.Year()

	cases := []struct{ name, tmpl, want string }{
		{"if true branch", `{{ if .Query.Q }}yes{{ else }}no{{ end }}`, "yes"},
		{"range join", `{{ join .Categories "," }}`, "42,54"},
		{"re_replace", `{{ re_replace .Query.Q "\\." " " }}`, "some movie 2024"},
		{"today year", `{{ .Today.Year }}`, "2026"},
	}
	for _, tc2 := range cases {
		t.Run(tc2.name, func(t *testing.T) {
			got, err := render(tc2.tmpl, tc)
			require.NoError(t, err)
			assert.Equal(t, tc2.want, got)
		})
	}
}

func TestResolveSettingsCheckboxSentinelsRoundTripThroughEqTemplate(t *testing.T) {
	def := &Definition{Settings: []SettingsField{
		{Name: "disablesort", Type: "checkbox", Default: "false"},
		{Name: "sort", Type: "select", Default: "time", Options: map[string]string{"time": "created", "seeders": "seeders"}},
	}}
	values, err := def.ResolveSettings("https://1337x.to/", map[string]string{}) // no override -> defaults apply
	require.NoError(t, err)
	assert.Equal(t, "https://1337x.to/", values["sitelink"])
	assert.Equal(t, "", values["disablesort"]) // unchecked -> False sentinel
	assert.Equal(t, "time", values["sort"])    // select -> chosen key, not the label

	values2, err := def.ResolveSettings("https://1337x.to/", map[string]string{"disablesort": "true"})
	require.NoError(t, err)
	assert.Equal(t, "true", values2["disablesort"])

	tc := &TemplateContext{Config: values, True: "true", False: ""}
	got, err := render(`{{ if eq .Config.disablesort .False }}unchecked{{ else }}checked{{ end }}`, tc)
	require.NoError(t, err)
	assert.Equal(t, "unchecked", got)
}

// TestBalanceActionParensFixesTheReal1337xTypo loads the real, unmodifiable
// testdata/cardigann/1337x.yml and proves balanceActionParens fixes the
// genuine typo in its second search.paths entry (the TV page): a stray
// extra ")" in `(eq .Config.disablesort .False))`. The fixed text's
// occurrence of that shared action must come out byte-identical to the
// well-formed sibling paths' own occurrence of the exact same action —
// not merely "no longer erroring".
func TestBalanceActionParensFixesTheReal1337xTypo(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "data", "cardigann", "1337x.yml"))
	require.NoError(t, err)
	def, err := Load(data)
	require.NoError(t, err)
	require.Len(t, def.Search.Paths, 4)

	moviesPath := def.Search.Paths[0].Path // well-formed
	tvPath := def.Search.Paths[1].Path     // has the real ")" typo

	require.NotEqual(t, moviesPath, tvPath, "fixture assumption: the two paths must actually differ")
	_, err = template.New("cardigann").Funcs(funcMap).Parse(tvPath)
	require.Error(t, err, "fixture assumption: the raw TV path must fail to parse")

	fixed, ok := balanceActionParens(tvPath)
	require.True(t, ok)

	const sharedAction = `{{ if and (.Keywords) (eq .Config.disablesort .False) }}`
	require.Equal(t, 2, strings.Count(moviesPath, sharedAction), "sanity: Movies path uses this action twice")
	assert.Equal(t, strings.Count(moviesPath, sharedAction), strings.Count(fixed, sharedAction),
		"the balanced TV path must contain byte-identical occurrences of the sibling paths' shared action")

	_, err = template.New("cardigann").Funcs(funcMap).Parse(fixed)
	require.NoError(t, err, "the balanced text must now actually parse")
}

// TestBalanceActionParensLeavesMoreOpensThanClosesUnchanged asserts a
// template with an unclosed group (more "(" than ")") — a different, real
// error this package must not try to paper over — is left byte-identical
// and still fails to parse.
func TestBalanceActionParensLeavesMoreOpensThanClosesUnchanged(t *testing.T) {
	in := `{{ if (and .A .B }}x{{ end }}`
	out, ok := balanceActionParens(in)
	assert.False(t, ok)
	assert.Equal(t, in, out)

	_, err := template.New("cardigann").Funcs(funcMap).Parse(out)
	require.Error(t, err)
}

// TestBalanceActionParensLeavesNonAdjacentExtraParenUnchanged asserts an
// unbalanced action whose extra ")" is not part of a doubled "))" run
// (the only pattern balanceActionParens ever collapses) is left
// byte-identical, not "fixed" some other way.
func TestBalanceActionParensLeavesNonAdjacentExtraParenUnchanged(t *testing.T) {
	in := `{{ if (.A) .B) }}x{{ end }}`
	require.Equal(t, 1, strings.Count(in, "("))
	require.Equal(t, 2, strings.Count(in, ")"))
	require.NotContains(t, in, "))")

	out, ok := balanceActionParens(in)
	assert.False(t, ok)
	assert.Equal(t, in, out)
}

// TestBalanceActionParensDoesNotMaskAnUnrelatedParseError asserts that a
// template broken for a reason other than a doubled ")" — an unknown
// function name (parens already balanced, nothing to try), or an action
// with no closing "}}" at all — surfaces render()'s own real, original
// parse error unchanged, rather than a paren-balancing side effect
// silently swallowing or altering it.
func TestBalanceActionParensDoesNotMaskAnUnrelatedParseError(t *testing.T) {
	cases := []string{
		`{{ nosuchfunction .X }}`, // unknown function; 0 "(" and 0 ")": balanced already
		`{{ if .X `,               // unclosed action: no "}}" anywhere
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, wantErr := template.New("cardigann").Funcs(funcMap).Parse(in)
			require.Error(t, wantErr)

			out, ok := balanceActionParens(in)
			assert.False(t, ok)
			assert.Equal(t, in, out)

			_, gotErr := render(in, &TemplateContext{})
			require.Error(t, gotErr)
			unwrapped := errors.Unwrap(gotErr)
			require.NotNil(t, unwrapped, "render must wrap the original parse error with %%w")
			assert.Equal(t, wantErr.Error(), unwrapped.Error())
		})
	}
}

// TestBalanceActionParensLeavesWellFormedTemplateUnchanged asserts a
// template whose parens already balance passes through byte-identical.
func TestBalanceActionParensLeavesWellFormedTemplateUnchanged(t *testing.T) {
	in := `{{ if and (.A) (eq .B .C) }}yes{{ else }}no{{ end }}`
	out, ok := balanceActionParens(in)
	assert.False(t, ok)
	assert.Equal(t, in, out)
}
