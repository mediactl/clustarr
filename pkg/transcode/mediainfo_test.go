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

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
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
				// A second audio track, at absolute ffprobe index 2, proves
				// AudioStream.Index is the audio-type-relative position (1)
				// ffmpeg's "0:a:1" expects, not the raw absolute index (2).
				Index:         2,
				CodecName:     "aac",
				CodecType:     "audio",
				Channels:      2,
				ChannelLayout: "stereo",
				SampleRate:    "48000",
				BitRate:       "128000",
				TagList:       ffprobe.Tags{"language": "spa"},
			},
			{
				// At absolute ffprobe index 3, proving SubtitleStream.Index
				// is likewise subtitle-type-relative (0), not absolute.
				Index:     3,
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

	require.Len(t, info.Audio, 2)
	a := info.Audio[0]
	require.Equal(t, int32(0), a.Index, "audio-type-relative, not the absolute ffprobe stream index (1)")
	require.Equal(t, "eac3", a.Codec)
	require.Equal(t, int32(6), a.Channels)
	require.Equal(t, "5.1(side)", a.ChannelLayout)
	require.Equal(t, int32(48000), a.SampleRate)
	require.Equal(t, int32(640), a.BitRateKbps)
	require.Equal(t, "eng", a.Language)
	require.True(t, a.Disposition.Default)
	require.False(t, a.Lossless)
	require.False(t, a.Atmos)
	a2 := info.Audio[1]
	require.Equal(t, int32(1), a2.Index, "audio-type-relative, not the absolute ffprobe stream index (2)")
	require.Equal(t, "aac", a2.Codec)
	require.Equal(t, "spa", a2.Language)

	require.Len(t, info.Subtitles, 1)
	s := info.Subtitles[0]
	require.Equal(t, int32(0), s.Index, "subtitle-type-relative, not the absolute ffprobe stream index (3)")
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

// TestFromProbePlusPlanReproducesTheHDR10Golden proves FromProbe's output
// feeds Plan/Args exactly like the hand-built MediaInfo in
// TestArgsGoldenHDR102160pCPU (args_test.go) does: same golden file, same
// argv. Neither commonv1.MediaInfo nor mediainfo.Raw carries the source
// path (Probe's caller already has it, having passed it in), so this test
// sets info.Path after FromProbe returns -- exactly what a real caller
// (the Phase E worker) does before calling Plan.
func TestFromProbePlusPlanReproducesTheHDR10Golden(t *testing.T) {
	raw := &mediainfo.Raw{
		Format: &ffprobe.Format{
			FormatName:      "matroska,webm",
			DurationSeconds: 7200,
			Size:            "20000000000",
			BitRate:         "22000000",
		},
		Streams: []*ffprobe.Stream{
			{
				Index:      0,
				CodecName:  "h264",
				CodecType:  "video",
				PixFmt:     "yuv420p10le",
				Width:      3840,
				Height:     2160,
				RFrameRate: "24000/1001",
			},
			{
				Index:         1,
				CodecName:     "aac",
				CodecType:     "audio",
				Channels:      2,
				ChannelLayout: "stereo",
				SampleRate:    "48000",
				BitRate:       "128000",
				Disposition:   ffprobe.StreamDisposition{Default: 1},
				TagList:       ffprobe.Tags{"language": "eng"},
			},
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
	mi := &commonv1.MediaInfo{Hdr: commonv1.HdrFormatHDR10, VideoBitDepth: 10}

	info, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	info.Path = "/media/movies/Example (2019)/Example (2019).mkv" // see doc comment above

	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierCPUx265, plan.Tier)

	assertGolden(t, "hdr10_2160p_cpu", transcode.Args(plan))
}
