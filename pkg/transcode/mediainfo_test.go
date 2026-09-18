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

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

func rawFixture() *mediainfo.Raw {
	return &mediainfo.Raw{
		Format: &ffprobe.Format{
			FormatName:      "matroska,webm",
			DurationSeconds: 7261.5,
			Size:            "12345678900",
			BitRate:         "13600000",
			TagList:         ffprobe.Tags{"title": "Example Movie"},
		},
		Streams: []*ffprobe.Stream{
			{
				Index:      0,
				CodecName:  "hevc",
				CodecType:  "video",
				Profile:    "Main 10",
				Level:      150,
				PixFmt:     "yuv420p10le",
				Width:      3840,
				Height:     2160,
				RFrameRate: "24000/1001",
				FieldOrder: "progressive",
				Duration:   "7261.500000",
				TagList:    ffprobe.Tags{"language": "eng", "title": "Feature"},
			},
			{
				Index:         1,
				CodecName:     "eac3",
				CodecType:     "audio",
				Channels:      6,
				ChannelLayout: "5.1(side)",
				SampleRate:    "48000",
				BitRate:       "640000",
				Disposition:   ffprobe.StreamDisposition{Default: 1},
				TagList:       ffprobe.Tags{"language": "eng"},
			},
			{
				Index:     2,
				CodecName: "hdmv_pgs_subtitle",
				CodecType: "subtitle",
				TagList:   ffprobe.Tags{"language": "eng"},
			},
		},
		Chapters: []*ffprobe.Chapter{
			{StartTimeSeconds: 0, EndTimeSeconds: 600, TagList: ffprobe.Tags{"title": "Chapter 1"}},
		},
		ColorPrimaries: "bt2020",
		ColorTransfer:  "smpte2084",
		ColorSpace:     "bt2020nc",
		ColorRange:     "tv",
		MasteringDisplay: &mediainfo.MasteringDisplay{
			GreenX: 13250, GreenY: 34500,
			BlueX: 7500, BlueY: 3000,
			RedX: 34000, RedY: 16000,
			WhiteX: 15635, WhiteY: 16450,
			MaxLuminance: 10000000, MinLuminance: 1,
		},
		ContentLight: &mediainfo.ContentLight{MaxCLL: 1000, MaxFALL: 400},
	}
}

func mediaInfoFixture() *commonv1.MediaInfo {
	return &commonv1.MediaInfo{Hdr: commonv1.HdrFormatHDR10, VideoBitDepth: 10}
}

func TestFromProbeMapsEveryField(t *testing.T) {
	raw := rawFixture()
	info, err := transcode.FromProbe(mediaInfoFixture(), raw)
	require.NoError(t, err)

	require.Equal(t, "matroska,webm", info.Format.Name)
	require.Equal(t, 7261500*time.Millisecond, info.Format.Duration)
	require.Equal(t, int64(12345678900), info.Format.SizeBytes)
	require.Equal(t, int64(13600), info.Format.BitRateKbps)
	require.Equal(t, "Example Movie", info.Tags["title"])

	require.Len(t, info.Video, 1)
	v := info.Video[0]
	require.Equal(t, int32(0), v.Index)
	require.Equal(t, "hevc", v.Codec)
	require.Equal(t, "Main 10", v.Profile)
	require.Equal(t, int32(150), v.Level)
	require.Equal(t, "yuv420p10le", v.PixFmt)
	require.Equal(t, int32(3840), v.Width)
	require.Equal(t, int32(2160), v.Height)
	require.Equal(t, transcode.Rational{Num: 24000, Den: 1001}, v.FrameRate)
	require.Equal(t, "progressive", v.FieldOrder)
	require.Equal(t, "bt2020", v.ColorPrimaries)
	require.Equal(t, "smpte2084", v.ColorTransfer)
	require.Equal(t, "bt2020nc", v.ColorSpace)
	require.Equal(t, "tv", v.ColorRange)
	require.Equal(t, 7261500*time.Millisecond, v.Duration)
	require.Equal(t, "eng", v.Language)
	require.Equal(t, "Feature", v.Title)
	require.Equal(t, int32(10), v.BitDepth)
	require.Equal(t, commonv1.HdrFormatHDR10, v.HDR.Format)
	require.Same(t, raw.ContentLight, v.HDR.ContentLight)
	require.Same(t, raw.MasteringDisplay, v.HDR.MasteringDisplay)
	require.Equal(t, int32(10000000), v.HDR.MasteringDisplay.MaxLuminance)
	require.Equal(t, int32(1000), v.HDR.ContentLight.MaxCLL)
	require.Nil(t, v.HDR.DolbyVision)
	require.False(t, v.HDR.HasHDR10Plus)

	require.Len(t, info.Audio, 1)
	a := info.Audio[0]
	require.Equal(t, int32(1), a.Index)
	require.Equal(t, "eac3", a.Codec)
	require.Equal(t, int32(6), a.Channels)
	require.Equal(t, "5.1(side)", a.ChannelLayout)
	require.Equal(t, int32(48000), a.SampleRate)
	require.Equal(t, int32(640), a.BitRateKbps)
	require.Equal(t, "eng", a.Language)
	require.True(t, a.Disposition.Default)
	require.False(t, a.Lossless)
	require.False(t, a.Atmos)

	require.Len(t, info.Subtitles, 1)
	s := info.Subtitles[0]
	require.Equal(t, int32(2), s.Index)
	require.Equal(t, "hdmv_pgs_subtitle", s.Codec)
	require.True(t, s.Bitmap)
	require.Equal(t, "eng", s.Language)

	require.Len(t, info.Chapters, 1)
	require.Equal(t, time.Duration(0), info.Chapters[0].Start)
	require.Equal(t, 600*time.Second, info.Chapters[0].End)
	require.Equal(t, "Chapter 1", info.Chapters[0].Title)
}

func TestFromProbeRejectsNilInputs(t *testing.T) {
	_, err := transcode.FromProbe(nil, rawFixture())
	require.Error(t, err)

	_, err = transcode.FromProbe(mediaInfoFixture(), nil)
	require.Error(t, err)
}

func TestFromProbeRejectsARawWithNoVideoStream(t *testing.T) {
	raw := rawFixture()
	raw.Streams = raw.Streams[1:] // drop the video stream, keep audio/subs
	_, err := transcode.FromProbe(mediaInfoFixture(), raw)
	require.Error(t, err)
}
