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

package mediainfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBitDepthFromPixFmt(t *testing.T) {
	cases := map[string]int32{
		"yuv420p": 8, "yuv420p10le": 10, "yuv420p12le": 12, "yuv444p10le": 10, "": 8,
	}
	for in, want := range cases {
		assert.Equal(t, want, bitDepthFromPixFmt(in), in)
	}
}

func TestFrameRateMilli(t *testing.T) {
	cases := map[string]int32{
		"24/1": 24000, "24000/1001": 23976, "30/1": 30000, "0/0": 0, "": 0,
	}
	for in, want := range cases {
		assert.Equal(t, want, frameRateMilli(in), in)
	}
}

func TestKbpsFromBitRate(t *testing.T) {
	assert.Equal(t, int32(477), kbpsFromBitRate("477224"))
	assert.Equal(t, int32(0), kbpsFromBitRate(""))
	assert.Equal(t, int32(0), kbpsFromBitRate("not-a-number"))
}

func TestContainerFromPath(t *testing.T) {
	assert.Equal(t, "mp4", containerFromPath("testdata/mediainfo/sample_h264_8bit.mp4"))
	assert.Equal(t, "mkv", containerFromPath("/data/movies/Foo/Foo.MKV"))
}
