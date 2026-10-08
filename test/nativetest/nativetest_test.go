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

package nativetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequiredFollowsTheEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name, tools, par2, what string
		want                    bool
	}{
		{"nothing set", "", "", "FFmpeg", false},
		{"tools required", "1", "", "FFmpeg", true},
		{"par2go only for par2go", "", "1", "FFmpeg", false},
		{"par2go required", "", "1", "par2go", true},
		{"tools cover par2go", "1", "", "par2go", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLUSTARR_REQUIRE_TOOLS", tt.tools)
			t.Setenv("PAR2GO_REQUIRE", tt.par2)
			t.Setenv("CLUSTARR_FFGO_REQUIRE", "")
			assert.Equal(t, tt.want, required(tt.what))
		})
	}
}

// CI's ffgo switch (upgrade guide U2.1) covers this package's FFmpeg gate too.
func TestTheFFgoRequirementCoversFFmpeg(t *testing.T) {
	t.Setenv("CLUSTARR_REQUIRE_TOOLS", "")
	t.Setenv("PAR2GO_REQUIRE", "")
	t.Setenv("CLUSTARR_FFGO_REQUIRE", "1")
	assert.True(t, required("FFmpeg 9 with the ffgo shim"))
	assert.False(t, required("par2go"))
}

func TestPinnedRefusesAHostFFmpeg(t *testing.T) {
	t.Setenv("CLUSTARR_REQUIRE_TOOLS", "1")
	t.Setenv("CLUSTARR_NATIVE_ASSETS", "/opt/native")
	assert.NoError(t, pinned("/opt/native/lib/libavcodec.so.63.1.100"))
	assert.ErrorContains(t, pinned("/usr/lib/libavcodec.so.63"), "not the pinned build")
	t.Setenv("CLUSTARR_NATIVE_ASSETS", "")
	assert.ErrorContains(t, pinned("/usr/lib/libavcodec.so.63"), "CLUSTARR_NATIVE_ASSETS")
	t.Setenv("CLUSTARR_REQUIRE_TOOLS", "")
	assert.NoError(t, pinned("/usr/lib/libavcodec.so.63"), "outside make, any FFmpeg 9 will do")
}

func TestVersionLineReadsLibavcodec(t *testing.T) {
	out := "ffmpeg version n9.0.2\n  libavutil      60.  3.100 / 60.  3.100\n  libavcodec     63.  1.100 / 63.  1.100\n"
	major, minor, ok := libavcodecVersion(out)
	assert.True(t, ok)
	assert.Equal(t, [2]int{63, 1}, [2]int{major, minor})
	_, _, ok = libavcodecVersion("ffprobe version n9.0.2\n  libavutil      60.  3.100\n")
	assert.False(t, ok, "no libavcodec line")
}
