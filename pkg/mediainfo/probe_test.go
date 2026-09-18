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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func skipIfNoFFprobe(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
}

func TestProbeH264MP4(t *testing.T) {
	skipIfNoFFprobe(t)

	mi, raw, err := Probe(context.Background(), "../../testdata/mediainfo/sample_h264_8bit.mp4")
	require.NoError(t, err)

	assert.Equal(t, "mp4", mi.Container)
	assert.Equal(t, "h264", mi.VideoCodec)
	assert.Equal(t, "Constrained Baseline", mi.VideoProfile)
	assert.Equal(t, int32(8), mi.VideoBitDepth)
	assert.Equal(t, int32(320), mi.Width)
	assert.Equal(t, int32(240), mi.Height)
	assert.Equal(t, int32(24000), mi.FpsMilli)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	require.Len(t, mi.Audio, 1)
	assert.Equal(t, "aac", mi.Audio[0].Codec)
	assert.Empty(t, mi.Subtitles)
	assert.Equal(t, int32(0), mi.Chapters)
	require.NotNil(t, raw.Format)
	assert.Contains(t, raw.Format.FormatName, "mp4")
	assert.Nil(t, raw.Dovi)
}

func TestProbeHEVC10bitMKV(t *testing.T) {
	skipIfNoFFprobe(t)

	mi, raw, err := Probe(context.Background(), "../../testdata/mediainfo/sample_hevc_10bit.mkv")
	require.NoError(t, err)

	assert.Equal(t, "mkv", mi.Container)
	assert.Equal(t, "hevc", mi.VideoCodec)
	assert.Equal(t, "Main 10", mi.VideoProfile)
	assert.Equal(t, "yuv420p10le", mi.PixelFormat)
	assert.Equal(t, int32(10), mi.VideoBitDepth)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	require.Len(t, mi.Audio, 1)
	assert.Equal(t, "ac3", mi.Audio[0].Codec)
	assert.Equal(t, int32(128), mi.Audio[0].BitrateKbps)
	require.Len(t, mi.Subtitles, 1)
	assert.Equal(t, "subrip", mi.Subtitles[0].Codec)
	assert.False(t, mi.Subtitles[0].Forced)
	assert.Contains(t, raw.Format.FormatName, "matroska")
	assert.Nil(t, raw.Dovi)
}

func TestProbeMissingFileReturnsWrappedError(t *testing.T) {
	skipIfNoFFprobe(t)

	_, _, err := Probe(context.Background(), "../../testdata/mediainfo/does-not-exist.mkv")
	require.Error(t, err)
}
