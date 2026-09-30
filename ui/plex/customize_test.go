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

package plex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func fullObject() map[string]any {
	return map[string]any{
		"ratingKey": "rk", "key": "/library/metadata/rk", "guid": "g", "type": "movie",
		"title": "Weekend", "year": 2011, "summary": "s", "tagline": "t",
		"Genre": []any{map[string]any{"tag": "Drama"}}, "Role": []any{map[string]any{"tag": "Tom"}},
		"Similar": []any{map[string]any{"guid": "tmdb://1"}},
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCustomizationFiltersFieldsAndElements(t *testing.T) {
	identity := []string{"ratingKey", "key", "guid", "type"}
	elements := []string{"Genre", "Role", "Similar"}
	cases := []struct {
		name string
		c    customization
		want []string
	}{
		{
			"includeFields keeps those fields, the identity and every element",
			customization{includeFields: set("title,year")},
			append(append([]string{"title", "year"}, identity...), elements...),
		},
		{
			"excludeFields drops them",
			customization{excludeFields: set("summary,tagline")},
			append(append([]string{"title", "year"}, identity...), elements...),
		},
		{
			"includeElements keeps only those elements and every field",
			customization{includeElements: set("Genre")},
			append([]string{"title", "year", "summary", "tagline", "Genre"}, identity...),
		},
		{
			"excludeElements drops them",
			customization{excludeElements: set("Role,Similar")},
			append([]string{"title", "year", "summary", "tagline", "Genre"}, identity...),
		},
		{
			"the identity fields always stay",
			customization{includeFields: set("title"), includeElements: set("none")},
			append([]string{"title"}, identity...),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, keys(tc.c.apply(fullObject())))
		})
	}
}

func TestCustomizationReachesIntoChildren(t *testing.T) {
	obj := fullObject()
	obj["Children"] = map[string]any{"size": 1, "Metadata": []any{fullObject()}}
	out := customization{includeFields: set("title"), includeElements: set("Children")}.apply(obj)
	child := out["Children"].(map[string]any)["Metadata"].([]any)[0].(map[string]any)
	assert.ElementsMatch(t, []string{"title", "ratingKey", "key", "guid", "type"}, keys(child),
		"a child keeps the included fields; its own elements are not Children, so they go")
	assert.Equal(t, 1, out["Children"].(map[string]any)["size"], "the Children container's own size stays")
}
