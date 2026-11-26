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
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// hdr10FixtureSpec is the recipe of the e2e fixture image's HDR10 clip
// (test/fixtures/mkclips' "hdr10.mkv", which images/Dockerfile.e2e-fixtures
// bakes in), as an internal/testmedia Spec: a short lavfi source encoded
// HEVC 10-bit with BT.2020/PQ colour tags and SMPTE ST 2086 mastering
// display and content light level metadata. Duplicated here because mkclips
// is a program; if the recipe changes there it changes here, and each names
// the other. The test below proves the recipe makes a file pkg/mediainfo
// reads back as HDR10 -- not PQ10 (the colour tags without the mastering
// display) and not SDR (colour tags that never reached the bitstream) --
// independent of the image build.
//
// The chromaticity, luminance and max-CLL values are
// docs/research/transcode.md §2.2's own worked example.
var hdr10FixtureSpec = testmedia.Spec{
	Video: []testmedia.Video{{
		Source: "testsrc2=duration=3:size=640x480:rate=25", Encoder: "libx265", PixFmt: "yuv420p10le",
		Options: map[string]string{"x265-params": "log-level=error"},
		Color:   testmedia.Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc"},
		HDR10: &testmedia.HDR10{
			MasteringDisplay: [10]uint32{13250, 34500, 7500, 3000, 34000, 16000, 15635, 16450, 10000000, 1},
			MaxCLL:           1000, MaxFALL: 400,
		},
	}},
}

// TestHDR10FixtureClassifiesAsHDR10 makes images/Dockerfile.e2e-fixtures's
// HDR10 clip as the image does and proves the full, real
// pkg/mediainfo/native Probe -> ClassifyHDR path reads it back as HDR10
// -- not PQ10 (which is what a clip with the colour transfer but no
// mastering-display metadata would classify as), not SDR (what a clip whose
// colour tags never reached the bitstream at all would classify as), and not
// HDR10+ or Dolby Vision. This is what makes the Dockerfile stage's clip
// meaningful: a clip ffmpeg writes but ClassifyHDR reads as SDR proves
// nothing about the fixture image.
func TestHDR10FixtureClassifiesAsHDR10(t *testing.T) {
	p := testProber(t)
	path := hdr10Clip(t)

	mi, raw, err := p.Probe(context.Background(), path)
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

	require.Equal(t, commonv1.HdrFormatHDR10, mediainfo.ClassifyHDR(raw))
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "toMediaInfo must mirror ClassifyHDR's verdict onto MediaInfo.Hdr")
}
