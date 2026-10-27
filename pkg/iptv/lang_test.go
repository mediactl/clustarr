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

package iptv_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/iptv"
)

// The provider's language setting is a BCP-47 tag or an ISO 639-2 code; the
// program map wants three letters, and a sentinel names no language.
func TestISO6392(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"en", "eng", true},
		{"eng", "eng", true},
		{"en-US", "eng", true},
		{"pt-BR", "por", true},
		{"es", "spa", true},
		{"fre", "fra", true}, // ISO 639-2/B read as 639-2/T
		{"ja", "jpn", true},
		{"", "", false},
		{"und", "", false},
		{"zxx", "", false},
		{"not a language", "", false},
	} {
		got, ok := iptv.ISO6392(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}
