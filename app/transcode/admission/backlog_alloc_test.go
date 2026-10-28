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

package admission

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// allocsPerPass is a pass's allocations over a full window of 3 and a
// backlog of n files.
func allocsPerPass(t *testing.T, n int) float64 {
	t.Helper()
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0}, 3)
	for i := range 3 {
		l.Observe(fv(fmt.Sprintf("open-%d", i), 1, pending))
	}
	for i := range n {
		l.Observe(fv(fmt.Sprintf("the-sopranos-1999-a1b2c3d4e5-s01e01-%05d", i), 1, ""))
	}
	in := passIn()
	l.Pass(in)
	return testing.AllocsPerRun(5, func() { l.Pass(in) })
}

// A pass over a full window sorts no backlog: it neither grants a window
// place nor needs the backlog's order to count it. On talos (2026-10-28) the
// pass ran 2.5 times a second over a hevc-mkv backlog of 2,289 files, sorting
// it twice each time and building two keys per comparison, about 0.14 of the
// manager's cores; a full window is the steady state.
func TestAPassOverAFullWindowSortsNoBacklog(t *testing.T) {
	small, large := allocsPerPass(t, 20), allocsPerPass(t, 2000)
	assert.LessOrEqual(t, large-small, float64(2*(2000-20)),
		"a pass over a full window allocated %.0f with 2,000 files waiting and %.0f with 20", large, small)
}
