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
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestVerifyEndToEndAgainstRealFfprobe(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffprobe"); err != nil {
		t.Skip("ffprobe not present on this box")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-shortest", src)
	require.NoError(t, gen.Run())

	// dst == src here: this step only proves Verifier's ffprobe plumbing and
	// JSON parsing are correct, independent of Runner (Step 14 already
	// covers the real Runner -> Verify pipeline together).
	exp := transcode.Expectation{
		DurationTolMillis: 200, Streams: 2, VideoCodec: "h264", PixelFormat: "yuv420p", MinOutputBytes: 512,
	}
	report, err := transcode.NewVerifier("/usr/bin/ffprobe").Verify(context.Background(), src, src, exp)
	require.NoError(t, err)
	require.True(t, report.OK, "problems: %v", report.Problems)
	require.InDelta(t, 2000, report.DurationMillis, 100)
}
