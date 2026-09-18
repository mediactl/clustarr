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
	"testing"
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
