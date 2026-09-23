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
package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestDistinctAltTitles(t *testing.T) {
	cases := []struct {
		name    string
		primary string
		in      []metadata.AltTitle
		want    []metadata.AltTitle
	}{
		{name: "nothing in, nothing out", primary: "Inception"},
		{
			name:    "the primary title, in any case or punctuation, is not an alternate",
			primary: "Game of Thrones",
			in:      []metadata.AltTitle{{Title: "Game of Thrones"}, {Title: "game of thrones!"}, {Title: "GoT"}},
			want:    []metadata.AltTitle{{Title: "GoT"}},
		},
		{
			name:    "duplicates keep the first, with its language and country",
			primary: "Inception",
			in: []metadata.AltTitle{
				{Title: "A Origem", Country: "BR"},
				{Title: "A Origem", Country: "PT"},
				{Title: "a origem.", Country: "AO"},
				{Title: "Origen", Country: "ES"},
			},
			want: []metadata.AltTitle{{Title: "A Origem", Country: "BR"}, {Title: "Origen", Country: "ES"}},
		},
		{
			name:    "blank titles are dropped and titles are trimmed",
			primary: "Inception",
			in:      []metadata.AltTitle{{Title: ""}, {Title: "   "}, {Title: "  Origen  ", Language: "es"}},
			want:    []metadata.AltTitle{{Title: "Origen", Language: "es"}},
		},
		{
			name:    "non-Latin scripts key by their own letters",
			primary: "Inception",
			in:      []metadata.AltTitle{{Title: "盗梦空间"}, {Title: "Начало"}, {Title: "начало"}},
			want:    []metadata.AltTitle{{Title: "盗梦空间"}, {Title: "Начало"}},
		},
		{
			name:    "a title of punctuation alone is compared exactly",
			primary: "Inception",
			in:      []metadata.AltTitle{{Title: "!!!"}, {Title: "???"}, {Title: "!!!"}},
			want:    []metadata.AltTitle{{Title: "!!!"}, {Title: "???"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, metadata.DistinctAltTitles(tc.primary, tc.in))
		})
	}
}
