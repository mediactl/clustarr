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

package native

import (
	"testing"
)

// testProber is a Prober for a test, which skips without FFmpeg 9 and the
// shim. T2a's test/nativetest (W7.3) replaces the skip with
// nativetest.FFmpeg(t, Needs), which fails instead where FFmpeg is required.
func testProber(t *testing.T) *Prober {
	t.Helper()
	p, err := New()
	if err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	return p
}

// The seams above, for the package's external tests (package native_test):
// a test that runs pkg/transcode/engine cannot be in package native, which
// the engine imports.
var (
	ProberForTest         = testProber
	FailFirstFrameForTest = failFirstFrame
)
