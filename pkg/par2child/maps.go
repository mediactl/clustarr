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

package par2child

import (
	"os"
	"path/filepath"
	"strings"
)

// Library is the path of the libpar2shim.so this process mapped, "" before
// par2go has loaded it. par2go exports no accessor for it, so it is read
// from /proc/self/maps, as ffruntime.Report does for FFmpeg.
func Library() string {
	b, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 6 {
			if p := strings.Join(f[5:], " "); strings.HasPrefix(filepath.Base(p), "libpar2shim.so") {
				return p
			}
		}
	}
	return ""
}
