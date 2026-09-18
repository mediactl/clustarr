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

package transcode_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestCompareProbesFlagsEachViolationIndependently(t *testing.T) {
	exp := transcode.Expectation{
		DurationTolMillis: 500, Streams: 2, VideoCodec: "hevc", PixelFormat: "yuv420p10le", MinOutputBytes: 1024,
	}
	cases := []struct {
		name        string
		srcMillis   int64
		dstMillis   int64
		dstStreams  int32
		dstCodec    string
		dstPixFmt   string
		dstSize     int64
		wantOK      bool
		wantProblem string
	}{
		{"all good", 3000, 3020, 2, "hevc", "yuv420p10le", 2048, true, ""},
		{"duration drifted too far", 3000, 4000, 2, "hevc", "yuv420p10le", 2048, false, "duration"},
		{"stream count mismatch", 3000, 3020, 1, "hevc", "yuv420p10le", 2048, false, "stream"},
		{"wrong codec", 3000, 3020, 2, "h264", "yuv420p10le", 2048, false, "codec"},
		{"wrong pix_fmt", 3000, 3020, 2, "hevc", "yuv420p8", 2048, false, "pix_fmt"},
		{"too small to be real", 3000, 3020, 2, "hevc", "yuv420p10le", 10, false, "size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := transcode.CompareProbes(tc.srcMillis, tc.dstMillis, tc.dstStreams, tc.dstCodec, tc.dstPixFmt, tc.dstSize, exp)
			require.Equal(t, tc.wantOK, r.OK)
			if !tc.wantOK {
				require.NotEmpty(t, r.Problems)
				found := false
				for _, p := range r.Problems {
					if strings.Contains(p, tc.wantProblem) {
						found = true
					}
				}
				require.True(t, found, "problems %v should mention %q", r.Problems, tc.wantProblem)
			}
		})
	}
}
