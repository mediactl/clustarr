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
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// hdr10FixtureArgs is the EXACT ffmpeg recipe
// images/Dockerfile.e2e-fixtures's clipgen-hdr10 stage bakes into the e2e
// fixture image (Task E-5). Duplicated here rather than shelling out to
// docker: this is a unit test, and the point is to prove the RECIPE itself
// -- generate a short lavfi source, encode it HEVC 10-bit with BT.2020/PQ
// colour tags and SMPTE ST 2086 mastering-display / content-light metadata
// via -x265-params -- produces a file pkg/mediainfo actually reads back as
// HDR10, independent of the Docker build. If this recipe ever changes, the
// Dockerfile stage's own copy must change identically; both carry a comment
// pointing at the other.
//
// colorprim/transfer/colormatrix and master-display/max-cll are ALL set
// through -x265-params, not ffmpeg's generic -color_primaries/-color_trc/
// -colorspace output options: verified empirically that the generic options
// reach libx265's VUI for colorspace alone (colour_space alone would ffprobe
// back), not for primaries or transfer -- a clip built with those instead
// would decode with color_transfer entirely absent from the frame, and
// ClassifyHDR would call it PQ10 (color_transfer==""; falls to
// HdrFormatNone) rather than HDR10. That is exactly the "ffmpeg writes it,
// ClassifyHDR reads SDR" trap this task's brief warns against, and this test
// exists to catch it if it ever regresses.
//
// The chromaticity/luminance/max-CLL values are
// docs/research/transcode.md §2.2's own worked example, so a correctly
// generated clip decodes to the exact side data
// pkg/mediainfo/ffprobe_test.go's hdr10FrameJSON fixture already
// hand-authored from a real ffprobe capture.
var hdr10FixtureArgs = []string{
	"-hide_banner", "-loglevel", "error", "-y",
	"-f", "lavfi", "-i", "testsrc2=duration=3:size=640x480:rate=25",
	"-c:v", "libx265", "-pix_fmt", "yuv420p10le",
	"-x265-params", "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:" +
		"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
}

// TestHDR10FixtureClassifiesAsHDR10 generates images/Dockerfile.e2e-fixtures's
// HDR10 clip with a real ffmpeg and proves the full, real pkg/mediainfo.Probe
// -> ClassifyHDR path reads it back as HDR10 -- not PQ10 (which is what a
// clip with the colour transfer but no mastering-display metadata would
// classify as), not SDR (what a clip whose colour tags never reached the
// bitstream at all would classify as), and not HDR10+ or Dolby Vision. This
// is what makes the Dockerfile stage's clip meaningful: a clip ffmpeg writes
// but ClassifyHDR reads as SDR proves nothing about the fixture image.
func TestHDR10FixtureClassifiesAsHDR10(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	skipIfNoFFprobe(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "hdr10.mkv")
	args := append(append([]string{}, hdr10FixtureArgs...), path)

	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)

	mi, raw, err := Probe(context.Background(), path)
	require.NoError(t, err)

	require.Equal(t, "hevc", mi.VideoCodec)
	require.Equal(t, "yuv420p10le", mi.PixelFormat)
	require.Equal(t, int32(10), mi.VideoBitDepth)

	// The three inputs ClassifyHDR's HDR10 branch requires, checked
	// individually so a failure here names exactly which piece of the
	// recipe stopped reaching the bitstream, not just the final verdict.
	require.Nil(t, raw.Dovi, "no Dolby Vision side data was requested")
	require.False(t, raw.HasHDR10Plus, "no HDR10+ dynamic metadata was requested")
	require.Equal(t, "smpte2084", raw.ColorTransfer,
		"the PQ transfer function must reach the decoded frame (via -x265-params transfer=, not -color_trc)")
	require.NotNil(t, raw.MasteringDisplay,
		"SMPTE ST 2086 mastering-display side data must reach the decoded frame (via -x265-params master-display=)")
	require.NotNil(t, raw.ContentLight,
		"MaxCLL/MaxFALL content-light side data must reach the decoded frame (via -x265-params max-cll=)")

	require.Equal(t, commonv1.HdrFormatHDR10, ClassifyHDR(raw))
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "toMediaInfo must mirror ClassifyHDR's verdict onto MediaInfo.Hdr")
}
