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

package testmedia

import (
	"os"
	"sync"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avdevice"
)

// Require skips t unless FFmpeg 9's libraries and a versioned libffshim
// load. With CLUSTARR_FFGO_REQUIRE=1 (CI) or CLUSTARR_REQUIRE_TOOLS=1 (make
// test) a missing library fails t, so a test that needs ffgo can never pass
// by skipping (upgrade guide U2.1).
func Require(t testing.TB) {
	t.Helper()
	miss := func(format string, args ...any) {
		if os.Getenv("CLUSTARR_FFGO_REQUIRE") == "1" || os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatalf("testmedia: "+format, args...)
		}
		t.Skipf("testmedia: "+format, args...)
	}
	if err := ffgo.Init(); err != nil {
		miss("no FFmpeg libraries: %v", err)
	}
	if _, avc, _ := ffgo.Version(); avc>>16 != 63 {
		miss("libavcodec %d is not FFmpeg 9's 63", avc>>16)
	}
	if ffgo.ShimAPIVersion() < 1 {
		miss("libffshim has no API version (want the clustarr/unify-media fork's shim, FFSHIM_API_VERSION >= 1)")
	}
	if err := registerDevices(); err != nil { // lavfi sources
		miss("libavdevice: %v", err)
	}
}

var (
	devicesOnce sync.Once
	devicesErr  error
)

// registerDevices registers libavdevice's input devices once, so lavfi
// sources open; it keeps the first error.
func registerDevices() error {
	devicesOnce.Do(func() {
		if devicesErr = ffgo.Init(); devicesErr != nil {
			return
		}
		devicesErr = avdevice.RegisterAll()
	})
	return devicesErr
}
