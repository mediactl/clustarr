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

package transcode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestX265ParamsIsExportedAndDeterministic pins the name spec section 7
// gives this symbol ("func X265Params(...) string // golden-tested"). It
// takes an unexported hdrBucket, so the test is an internal one; the
// rendered string itself is covered by the -x265-params block of every
// CPU-tier golden in args_test.go.
func TestX265ParamsIsExportedAndDeterministic(t *testing.T) {
	v := VideoSpec{Refs: 4, RCLookahead: 40}
	vs := VideoStream{ColorRange: "tv"}

	got := X265Params(8, v, vs, hdrNone, DolbyVisionPassthrough)
	require.Contains(t, got, "pools=8")
	require.Contains(t, got, "ref=4")
	require.Contains(t, got, "rc-lookahead=40")

	// ExtraX265Params is appended sorted by key, never by ranging the map.
	v.ExtraX265Params = map[string]string{"zzz": "1", "aaa": "2", "mmm": "3"}
	for range 20 {
		assert.Equal(t, X265Params(8, v, vs, hdrNone, DolbyVisionPassthrough), X265Params(8, v, vs, hdrNone, DolbyVisionPassthrough))
	}
	withExtras := X265Params(8, v, vs, hdrNone, DolbyVisionPassthrough)
	assert.Less(t, indexOfParam(withExtras, "aaa="), indexOfParam(withExtras, "mmm="))
	assert.Less(t, indexOfParam(withExtras, "mmm="), indexOfParam(withExtras, "zzz="))
}

func indexOfParam(s, want string) int {
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
			return i
		}
	}
	return -1
}
