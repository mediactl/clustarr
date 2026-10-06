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

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/quality"
)

func TestLacksLanguage(t *testing.T) {
	orig := quality.Profile{Language: "original"}
	cases := []struct {
		name  string
		p     quality.Profile
		orig  string
		audio []string
		want  bool
	}{
		{"korean-only file of a japanese show", orig, "ja", []string{"ko"}, true},
		{"dual audio carries the original", orig, "ja", []string{"en", "ja"}, false},
		{"region subtags compare by base", quality.Profile{Language: "pt"}, "", []string{"pt-BR"}, false},
		{"unknown audio is not wrong", orig, "ja", nil, false},
		{"unknown original constrains nothing", orig, "", []string{"ko"}, false},
		{"any wants nothing", quality.Profile{Language: "any"}, "ja", []string{"ko"}, false},
		{"empty wants nothing", quality.Profile{}, "ja", []string{"ko"}, false},
		{"an explicit tag", quality.Profile{Language: "en"}, "ja", []string{"ja"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, LacksLanguage(c.p, c.orig, c.audio)) })
	}
}
