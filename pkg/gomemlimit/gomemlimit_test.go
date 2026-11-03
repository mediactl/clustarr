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

package gomemlimit

import (
	"runtime/debug"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShareIsARatioAutomemlimitAccepts(t *testing.T) {
	for _, tc := range []struct {
		percent int
		want    string
	}{
		{80, "0.80"}, {60, "0.60"}, {50, "0.50"}, {100, "1.00"},
	} {
		got := Share(tc.percent)
		assert.Equal(t, tc.want, got)
		r, err := strconv.ParseFloat(got, 64)
		require.NoError(t, err)
		assert.True(t, r > 0 && r <= 1, "%s is in (0, 1]", got)
	}
}

// TestAnExplicitGOMEMLIMITWins: the chart's goMemLimit renders $GOMEMLIMIT,
// which Set leaves as the runtime read it.
func TestAnExplicitGOMEMLIMITWins(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	t.Setenv("GOMEMLIMIT", "720MiB")
	debug.SetMemoryLimit(720 << 20)

	Set()

	assert.Equal(t, int64(720<<20), debug.SetMemoryLimit(-1))
}
