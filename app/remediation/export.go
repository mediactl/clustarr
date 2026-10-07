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

package remediation

import (
	"io/fs"
	"time"
)

// Test seams, for remediationtest and the loop's own envtests: nothing in a
// binary calls them.

// ShareLimiter makes dst pace its status writes through src's limiter, so
// two reconcilers in one test draw from one bucket. Test seam.
func ShareLimiter(dst, src *Reconciler) { dst.limiter = src.limiter }

// NewIOExecutorWith is NewIOExecutor with stat in place of os.Stat and both
// timeouts set to timeout. Test seam.
func NewIOExecutorWith(workers int, timeout time.Duration, stat func(string) (fs.FileInfo, error)) *IOExecutor {
	x := NewIOExecutor(workers)
	x.StatTimeout, x.ReadDirTimeout = timeout, timeout
	if stat != nil {
		x.stat = stat
	}
	return x
}

// NewIOExecutorFuncs is NewIOExecutor with stat and readDir in place of
// os.Stat and os.ReadDir (nil keeps the default), so a test can count or hang
// the calls a planner's Gather makes. Test seam.
func NewIOExecutorFuncs(workers int, stat func(string) (fs.FileInfo, error), readDir func(string) ([]fs.DirEntry, error)) *IOExecutor {
	x := NewIOExecutor(workers)
	if stat != nil {
		x.stat = stat
	}
	if readDir != nil {
		x.readDir = readDir
	}
	return x
}
