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

package fsops

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUmask(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
		bad  bool
	}{
		{in: "", ok: false},
		{in: "  ", ok: false},
		{in: "002", want: 0o002, ok: true},
		{in: "0002", want: 0o002, ok: true},
		{in: "0o027", want: 0o027, ok: true},
		{in: "22", want: 0o022, ok: true},
		{in: "777", want: 0o777, ok: true},
		{in: "1000", bad: true},
		{in: "008", bad: true},
		{in: "-1", bad: true},
		{in: "u=rwx", bad: true},
	} {
		got, ok, err := ParseUmask(tc.in)
		if tc.bad {
			require.Error(t, err, "ParseUmask(%q)", tc.in)
			continue
		}
		require.NoError(t, err, "ParseUmask(%q)", tc.in)
		require.Equal(t, tc.ok, ok, "ParseUmask(%q) ok", tc.in)
		require.Equal(t, tc.want, got, "ParseUmask(%q)", tc.in)
	}
}
