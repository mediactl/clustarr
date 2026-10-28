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

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSetIDReadsAnIdOrASetURL(t *testing.T) {
	for in, want := range map[string]string{
		"": "", " 41471 ": "41471", "https://mediux.pro/sets/41471": "41471", "mediux.pro/sets/38985/": "38985",
		"https://www.mediux.pro/sets/41471?tab=yaml": "41471",
		// The links the item page shows since 2026-10-27, AURA's.
		"https://mediux.io/show-set/41471": "41471", "mediux.io/movie-set/59100/": "59100", "https://mediux.io/collection-set/2133#x": "2133",
	} {
		got, err := ParseSetID(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"sharivari", "https://mediux.pro/user/sharivari", "4147a", "https://theposterdb.com/set/1", "https://mediux.io/boxset/12", "https://mediux.io/user-set/1"} {
		_, err := ParseSetID(in)
		assert.Error(t, err, in)
	}
}
