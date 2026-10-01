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

package standard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func video(codec, pixfmt string, bits int32, hdr commonv1.HdrFormat) transcode.VideoStream {
	return transcode.VideoStream{
		Index: 0, Codec: codec, PixFmt: pixfmt, BitDepth: bits, Width: 1920, Height: 1080,
		FrameRate: transcode.Rational{Num: 24000, Den: 1001}, HDR: transcode.HDRInfo{Format: hdr}, Duration: time.Hour,
	}
}

func audio(i int32, codec string, ch int32, layout, lang string) transcode.AudioStream {
	return transcode.AudioStream{Index: i, Codec: codec, Channels: ch, ChannelLayout: layout, SampleRate: 48000, Language: lang}
}

func info(v transcode.VideoStream, a ...transcode.AudioStream) transcode.MediaInfo {
	return transcode.MediaInfo{
		Path: "/data/m.mkv", Format: transcode.FormatInfo{Name: "matroska,webm", Duration: time.Hour},
		Video: []transcode.VideoStream{v}, Audio: a,
		Subtitles:   []transcode.SubtitleStream{{Index: 0, Codec: "subrip", Language: "eng"}, {Index: 1, Codec: "hdmv_pgs_subtitle", Bitmap: true}},
		Attachments: []transcode.AttachmentStream{{Index: 0, Filename: "f.ttf"}},
		Chapters:    []transcode.Chapter{{Start: 0, End: time.Minute, Title: "One"}},
	}
}

var (
	profile = Profile{Name: "hevc-mkv", Hash: "abc", Quality: 24, Container: transcode.ContainerMKV}
	cpu     = Hardware{Tier: transcode.TierCPUx265}
	nvenc   = Hardware{Tier: transcode.TierNVENC}
	h264    = video("h264", "yuv420p", 8, commonv1.HdrFormatNone)
	hevc10  = video("hevc", "yuv420p10le", 10, commonv1.HdrFormatNone)
	eac3    = audio(0, "eac3", 6, "5.1(side)", "eng")
)

func TestSDRH264EncodesOnTheCPU(t *testing.T) {
	p := Plan(info(h264, eac3), profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	assert.Equal(t, "libx265", p.Video.Encoder)
	assert.Equal(t, "24", p.Video.Options["crf"])
	assert.Equal(t, "slow", p.Video.Options["preset"])
	assert.Equal(t, "cpu", p.Video.Decode)
	assert.Equal(t, "format=yuv420p", p.Video.Filter, "SDR at 1080p is the 8-bit target")
	assert.Equal(t, "main", p.Video.Options["profile"])
	assert.Equal(t, "yuv420p", p.Expect.PixelFormat)
	assert.Equal(t, "sdr", p.Video.HDR)
	assert.Equal(t, []AudioPlan{{SourceIndex: 0, Action: "copy", Language: "eng"}}, p.Audio)
	assert.Equal(t, []int32{0, 1}, p.Subtitles)
	assert.True(t, p.Attachments)
	assert.True(t, p.Chapters)
	assert.Equal(t, "hevc-mkv@abc", p.Tags["CLUSTARR_PROFILE"])
}

func TestNVENCDecodesOnNVDECWithTheArchivalSettings(t *testing.T) {
	p := Plan(info(h264, eac3), profile, nvenc)
	require.Equal(t, DecisionEncode, p.Decision)
	assert.Equal(t, "hevc_nvenc", p.Video.Encoder)
	assert.Equal(t, "nvdec", p.Video.Decode)
	assert.Equal(t, "scale_cuda=format=nv12", p.Video.Filter)
	assert.Equal(t, map[string]string{"preset": "p7", "rc": "constqp", "qp": "23", "spatial-aq": "1", "temporal-aq": "1", "profile": "main"}, p.Video.Options)
}

func TestNVENCUploadsASourceNVDECCannotDecode(t *testing.T) {
	hi10p := video("h264", "yuv420p10le", 10, commonv1.HdrFormatNone)
	p := Plan(info(hi10p, eac3), profile, nvenc)
	assert.Equal(t, "upload", p.Video.Decode)
	assert.Equal(t, "hwupload,scale_cuda=format=nv12", p.Video.Filter)
}

func TestIntelTiers(t *testing.T) {
	q := Plan(info(h264, eac3), profile, Hardware{Tier: transcode.TierQSV})
	assert.Equal(t, "hevc_qsv", q.Video.Encoder)
	assert.Equal(t, "24", q.Video.Options["global_quality"])
	assert.Equal(t, "hwupload=extra_hw_frames=64,vpp_qsv=format=nv12", q.Video.Filter)
	assert.Equal(t, "main", q.Video.Options["profile"])
	v := Plan(info(h264, eac3), profile, Hardware{Tier: transcode.TierVAAPI})
	assert.Equal(t, "hevc_vaapi", v.Video.Encoder)
	assert.Equal(t, "24", v.Video.Options["qp"])
	assert.Equal(t, "hwupload,scale_vaapi=format=nv12", v.Video.Filter)
	assert.Equal(t, "main", v.Video.Options["profile"])
}

func TestQualityMapsPerEncoder(t *testing.T) {
	p30 := profile
	p30.Quality = 30
	assert.Equal(t, "30", Plan(info(h264, eac3), p30, cpu).Video.Options["crf"])
	assert.Equal(t, "29", Plan(info(h264, eac3), p30, nvenc).Video.Options["qp"])
}

func TestCompliantVideoAndAudioIsSkipped(t *testing.T) {
	p := Plan(info(hevc10, eac3, audio(1, "aac", 2, "stereo", "eng")), profile, cpu)
	assert.Equal(t, DecisionSkip, p.Decision)
}

func TestCompliantVideoWithTrueHDCopiesVideoAndEncodesAudio(t *testing.T) {
	p := Plan(info(hevc10, audio(0, "truehd", 8, "7.1", "eng")), profile, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, p.Reason)
	assert.Equal(t, "copy", p.Video.Action)
	assert.Equal(t, []AudioPlan{{SourceIndex: 0, Action: "aac", Channels: 6, Layout: "5.1", BitRate: 384000, Language: "eng"}}, p.Audio)
}

func TestHEVC8BitIsCompliantOnlyAtTheEightBitTarget(t *testing.T) {
	hevc8 := video("hevc", "yuv420p", 8, commonv1.HdrFormatNone)
	assert.Equal(t, DecisionSkip, Plan(info(hevc8, eac3), profile, cpu).Decision, "1080p SDR HEVC Main is the standard")
	hevc8.Width, hevc8.Height = 3840, 2160
	p := Plan(info(hevc8, eac3), profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, "above 1080p the standard is Main 10")
	assert.Equal(t, "main10", p.Video.Options["profile"])
	assert.Equal(t, "format=yuv420p10le", p.Video.Filter)
	assert.Equal(t, "yuv420p10le", p.Expect.PixelFormat)
}

func TestHDRAt1080pStaysMain10(t *testing.T) {
	hdr := video("h264", "yuv420p10le", 10, commonv1.HdrFormatHDR10)
	for _, hw := range []Hardware{cpu, nvenc} {
		p := Plan(info(hdr, eac3), profile, hw)
		require.Equal(t, DecisionEncode, p.Decision)
		assert.Equal(t, "main10", p.Video.Options["profile"], hw.Tier)
		assert.Equal(t, "yuv420p10le", p.Expect.PixelFormat, hw.Tier)
	}
}

func TestAudioRules(t *testing.T) {
	p := Plan(info(h264,
		audio(0, "eac3", 6, "5.1(side)", "eng"),
		audio(1, "flac", 2, "stereo", "eng"),
		audio(2, "dts", 8, "7.1", "eng"),
		audio(3, "ac3", 6, "5.1", "fre"),
		transcode.AudioStream{Index: 4, Codec: "aac", Channels: 2, Language: "fre", Title: "Director's Commentary", Disposition: transcode.Disposition{Comment: true}},
	), Profile{Name: "p", Hash: "h", Quality: 24, Container: transcode.ContainerMKV, Languages: []string{"eng"}}, cpu)
	assert.Equal(t, []AudioPlan{
		{SourceIndex: 0, Action: "copy", Language: "eng"},
		{SourceIndex: 1, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000, Language: "eng"},
		{SourceIndex: 2, Action: "aac", Channels: 6, Layout: "5.1", BitRate: 384000, Language: "eng"},
		{SourceIndex: 4, Action: "copy", Language: "fre", Title: "Director's Commentary", Comment: true},
	}, p.Audio, "fre AC-3 dropped by languages; the commentary kept")
}

func TestLanguagesNeverLeaveAFileSilent(t *testing.T) {
	p := Plan(info(h264, audio(0, "ac3", 6, "5.1", "jpn")), Profile{Name: "p", Hash: "h", Quality: 24, Languages: []string{"eng"}}, cpu)
	require.Len(t, p.Audio, 1, "no track matches eng: keep them all rather than write a silent file")
}

func TestHDR(t *testing.T) {
	hdr10 := video("hevc", "yuv420p10le", 10, commonv1.HdrFormatHDR10Plus)
	hdr10.HDR.MasteringDisplay = &mediainfo.MasteringDisplay{}
	p := Plan(info(hdr10, audio(0, "truehd", 8, "7.1", "eng")), profile, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, "HEVC Main 10 HDR10+ is compliant video: copied as is")
	p = Plan(info(video("h264", "yuv420p10le", 10, commonv1.HdrFormatHDR10), eac3), profile, cpu)
	assert.Equal(t, "hdr10", p.Video.HDR)
	assert.Equal(t, ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}, p.Video.Color)
	assert.Contains(t, p.Video.StripSideData, "hdr10plus")
	p = Plan(info(video("hevc", "yuv420p", 8, commonv1.HdrFormatHLG10), eac3), profile, cpu)
	assert.Equal(t, "hlg", p.Video.HDR)
	assert.Equal(t, "arib-std-b67", p.Video.Color.Transfer)
}

func TestDolbyVision(t *testing.T) {
	for _, c := range []struct {
		format   commonv1.HdrFormat
		decision Decision
		hdr      string
	}{
		{commonv1.HdrFormatDolbyVisionHDR10, DecisionEncode, "hdr10"}, // 7 and 8.1
		{commonv1.HdrFormatDolbyVisionHLG, DecisionEncode, "hlg"},     // 8.4
		{commonv1.HdrFormatDolbyVisionSDR, DecisionEncode, "sdr"},     // 8.2
		{commonv1.HdrFormatDolbyVision, DecisionSkip, ""},             // 5: no HDR10 or HLG base layer
	} {
		p := Plan(info(video("hevc", "yuv420p10le", 10, c.format), eac3), profile, cpu)
		assert.Equal(t, c.decision, p.Decision, c.format)
		if c.decision == DecisionEncode {
			assert.Equal(t, c.hdr, p.Video.HDR, c.format)
			assert.Contains(t, p.Video.StripSideData, "dovi", c.format)
		} else {
			assert.Contains(t, p.Reason, "Dolby Vision profile 5")
		}
	}
}

func TestDolbyVisionProfile7MapsOnlyTheBaseLayer(t *testing.T) {
	in := info(video("hevc", "yuv420p10le", 10, commonv1.HdrFormatDolbyVisionHDR10), eac3)
	el := video("hevc", "yuv420p10le", 10, commonv1.HdrFormatNone)
	el.Index, el.Width, el.Height = 1, 960, 540
	in.Video = append(in.Video, el)
	p := Plan(in, profile, cpu)
	assert.Equal(t, int32(0), p.Video.SourceIndex)
	assert.Equal(t, int32(1), p.Expect.VideoStreams, "the enhancement layer is not mapped")
}

func TestNeverTranscodeModifiersSkip(t *testing.T) {
	in := info(h264, eac3)
	in.Modifier = "remux"
	p := Plan(in, Profile{Name: "p", Hash: "h", Quality: 24, NeverTranscodeModifiers: []string{"remux"}}, cpu)
	assert.Equal(t, DecisionSkip, p.Decision)
	assert.Contains(t, p.Reason, "remux")
}

func TestHashIsStableAndSensitive(t *testing.T) {
	a := Plan(info(h264, eac3), profile, cpu)
	b := Plan(info(h264, eac3), profile, cpu)
	assert.Equal(t, a.Hash(), b.Hash())
	assert.Len(t, a.Hash(), 64)
	p30 := profile
	p30.Quality = 30
	assert.NotEqual(t, a.Hash(), Plan(info(h264, eac3), p30, cpu).Hash())
}

// FFmpeg's AAC encoder takes 5.1 with back surrounds ("5.1"), not
// "5.1(side)": the plan's AAC layout is the canonical one for the channel
// count, and the resampler maps the source's positions onto it.
func TestAACLayoutIsCanonicalForItsChannelCount(t *testing.T) {
	for _, c := range []struct {
		ch     int32
		layout string
	}{{1, "mono"}, {2, "stereo"}, {3, "3.0"}, {4, "4.0"}, {5, "5.0"}, {6, "5.1"}, {8, "5.1"}} {
		p := Plan(info(h264, audio(0, "flac", c.ch, "whatever(side)", "eng")), profile, cpu)
		assert.Equal(t, c.layout, p.Audio[0].Layout, "%d channels", c.ch)
	}
}

func TestMP4KeepsOnlyWhatItCanCarry(t *testing.T) {
	mp4 := profile
	mp4.Container = transcode.ContainerMP4
	in := info(h264, eac3)
	in.Subtitles = append(in.Subtitles, transcode.SubtitleStream{Index: 2, Codec: "mov_text", Language: "eng"})
	p := Plan(in, mp4, cpu)
	assert.Equal(t, []int32{2}, p.Subtitles, "SRT and PGS cannot be copied into MP4; mov_text can")
	assert.False(t, p.Attachments)
	assert.Equal(t, int32(1), p.Expect.SubtitleStreams)

	mkv := Plan(in, profile, cpu)
	assert.Equal(t, []int32{0, 1, 2}, mkv.Subtitles)
	assert.True(t, mkv.Attachments)
}

// A transcoded file is final (spec §5): one this profile wrote is skipped,
// and so is one written under an earlier hash (a profile edit, a new
// standard.Version) or by another profile -- re-transcoding is never an
// upgrade side effect.
func TestATranscodedFileIsSkippedWhateverItsTag(t *testing.T) {
	in := info(h264, audio(0, "truehd", 8, "7.1", "eng"))
	assert.Equal(t, DecisionEncode, Plan(in, profile, cpu).Decision, "untagged, it is encoded")
	for _, tag := range []string{profile.Name + "@" + profile.Hash, profile.Name + "@older", "other@" + profile.Hash} {
		in.Tags = map[string]string{"CLUSTARR_PROFILE": tag}
		p := Plan(in, profile, cpu)
		assert.Equal(t, DecisionSkip, p.Decision, tag)
		assert.Contains(t, p.Reason, "already transcoded", tag)
	}
}

// policy.minDuration (kept by the owner's API cut) skips a source shorter
// than it: trailers, extras, samples.
func TestASourceShorterThanMinDurationIsSkipped(t *testing.T) {
	p := profile
	p.MinDuration = 2 * time.Hour
	got := Plan(info(h264, eac3), p, cpu) // an hour long
	assert.Equal(t, DecisionSkip, got.Decision)
	assert.Contains(t, got.Reason, "policy.minDuration")
	p.MinDuration = time.Minute
	assert.Equal(t, DecisionEncode, Plan(info(h264, eac3), p, cpu).Decision)
}
