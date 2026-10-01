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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestFromSummaryMapsEveryPlanInput(t *testing.T) {
	profile, compat := int32(8), int32(1)
	mi := &commonv1.MediaInfo{
		Container: "matroska", VideoCodec: "h264", VideoProfile: "High 10", PixelFormat: "yuv420p10le",
		VideoBitDepth: 10, Width: 3840, Height: 2160, FpsMilli: 23976, VideoBitrateKbps: 40000,
		Hdr: commonv1.HdrFormatDolbyVisionHDR10, DoviProfile: &profile, DoviBLCompatID: &compat,
		RuntimeMillis: 7_261_500,
		Audio: []commonv1.AudioStream{
			{Index: 1, Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8, Language: "eng", Default: true},
			{Index: 2, Codec: "ac3", Channels: 2, Language: "eng", Title: "Director", Commentary: true},
		},
		Subtitles: []commonv1.SubtitleStream{
			{Index: 3, Codec: "subrip", Language: "eng", Forced: true},
			{Index: 4, Codec: "xsub", Language: "fre"}, // bitmap though pkg/mediainfo does not flag it
		},
		Attachments: 2,
	}

	info, err := transcode.FromSummary("/data/media/movies/X (2020)/X (2020).mkv", mi)
	require.NoError(t, err)

	assert.Equal(t, "/data/media/movies/X (2020)/X (2020).mkv", info.Path)
	assert.Equal(t, "matroska", info.Format.Name)
	assert.Equal(t, 7261500*time.Millisecond, info.Format.Duration)
	require.Len(t, info.Video, 1)
	v := info.Video[0]
	assert.Equal(t, "h264", v.Codec)
	assert.Equal(t, "High 10", v.Profile)
	assert.Equal(t, "yuv420p10le", v.PixFmt)
	assert.Equal(t, int32(2160), v.Height)
	assert.Equal(t, transcode.Rational{Num: 23976, Den: 1000}, v.FrameRate)
	assert.Equal(t, commonv1.HdrFormatDolbyVisionHDR10, v.HDR.Format)
	require.NotNil(t, v.HDR.DolbyVision)
	assert.Equal(t, int32(8), v.HDR.DolbyVision.Profile)
	assert.Equal(t, int32(1), v.HDR.DolbyVision.BLSignalCompatibilityID)
	assert.Equal(t, []string{"bt2020", "smpte2084", "bt2020nc", "tv"},
		[]string{v.ColorPrimaries, v.ColorTransfer, v.ColorSpace, v.ColorRange},
		"the colour an HDR10-family format is defined by")

	require.Len(t, info.Audio, 2)
	assert.Equal(t, int32(0), info.Audio[0].Index, "type-relative, not the container-wide 1")
	assert.True(t, info.Audio[0].Lossless)
	assert.True(t, info.Audio[0].Atmos)
	assert.True(t, info.Audio[0].Disposition.Default)
	assert.Equal(t, int32(1), info.Audio[1].Index)
	assert.True(t, info.Audio[1].Disposition.Comment)
	assert.Equal(t, "Director", info.Audio[1].Title)

	require.Len(t, info.Subtitles, 2)
	assert.Equal(t, int32(0), info.Subtitles[0].Index)
	assert.False(t, info.Subtitles[0].Bitmap)
	assert.True(t, info.Subtitles[0].Disposition.Forced)
	assert.True(t, info.Subtitles[1].Bitmap, "xsub is a bitmap codec to the renderer whatever the summary says")
	assert.Len(t, info.Attachments, 2)
}

func TestFromSummaryRefusesNoVideo(t *testing.T) {
	_, err := transcode.FromSummary("/x.mkv", nil)
	require.Error(t, err)
	_, err = transcode.FromSummary("/x.flac", &commonv1.MediaInfo{Container: "flac"})
	require.Error(t, err)
}
