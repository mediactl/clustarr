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
		// An image subtitle holds the whole file (TestAnImageSubtitleHoldsTheFile).
		Subtitles:   []transcode.SubtitleStream{{Index: 0, Codec: "subrip", Language: "eng"}, {Index: 1, Codec: "ass", Language: "eng"}},
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
	assert.Equal(t, []AudioPlan{
		{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true},
		{SourceIndex: 0, Action: "copy", Language: "eng"},
	}, p.Audio, "the AAC 2.0 companion first, then the E-AC-3 5.1 copied")
	assert.Equal(t, transcode.ContainerMP4, p.Container)
	assert.False(t, p.Attachments, "MP4 carries no attachments")
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

// The QSV tier decodes on VA-API a format its pod measured, the frames
// mapped to QSV for vpp_qsv and the encoder, and uploads anything else.
func TestQSVDecodesOnVAAPIAFormatItsPodMeasured(t *testing.T) {
	qsv := Hardware{Tier: transcode.TierQSV, Limits: transcode.Limits{
		VAAPI: &transcode.Decoders{Formats: map[string]bool{"h264:8": true, "h264:10": false}},
	}}
	p := Plan(info(h264, eac3), profile, qsv)
	assert.Equal(t, "vaapi", p.Video.Decode)
	assert.Equal(t, "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12", p.Video.Filter)
	assert.Equal(t, "hevc_qsv", p.Video.Encoder)

	hi10p := video("h264", "yuv420p10le", 10, commonv1.HdrFormatNone)
	p = Plan(info(hi10p, eac3), profile, qsv)
	assert.Equal(t, "cpu", p.Video.Decode)
	assert.Equal(t, "hwupload=extra_hw_frames=64,vpp_qsv=format=nv12", p.Video.Filter, "an SDR 1080p target is 8-bit")
}

func TestQualityMapsPerEncoder(t *testing.T) {
	p30 := profile
	p30.Quality = 30
	assert.Equal(t, "30", Plan(info(h264, eac3), p30, cpu).Video.Options["crf"])
	assert.Equal(t, "29", Plan(info(h264, eac3), p30, nvenc).Video.Options["qp"])
}

// mp4Info is an MP4 source with no subtitles or attachments: what the
// standard writes, when its audio is too.
func mp4Info(v transcode.VideoStream, a ...transcode.AudioStream) transcode.MediaInfo {
	in := info(v, a...)
	in.Format.Name = "mov,mp4,m4a,3gp,3g2,mj2"
	in.Subtitles, in.Attachments = nil, nil
	return in
}

func TestCompliantVideoAndAudioIsSkipped(t *testing.T) {
	p := Plan(mp4Info(hevc10, audio(0, "aac", 2, "stereo", "eng")), profile, cpu)
	assert.Equal(t, DecisionSkip, p.Decision, p.Reason)
	// The same file in Matroska is remuxed into MP4; so is one whose
	// surround track has no AAC companion yet.
	assert.Equal(t, DecisionCopyVideo, Plan(info(hevc10, audio(0, "aac", 2, "stereo", "eng")), profile, cpu).Decision)
	assert.Equal(t, DecisionCopyVideo, Plan(mp4Info(hevc10, eac3), profile, cpu).Decision)
}

func TestCompliantVideoWithTrueHDCopiesVideoAndEncodesAudio(t *testing.T) {
	p := Plan(info(hevc10, audio(0, "truehd", 8, "7.1", "eng")), profile, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, p.Reason)
	assert.Equal(t, "HEVC video copied into the MP4 layout; audio and subtitles processed", p.Reason)
	assert.Equal(t, "copy", p.Video.Action)
	assert.Equal(t, []AudioPlan{
		{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true},
		{SourceIndex: 0, Action: "ac3", Channels: 6, Layout: "5.1", BitRate: 640000, Language: "eng", Title: "Dolby Digital 5.1"},
	}, p.Audio)
}

// FLAC is never kept (the owner, 2026-10-15): stereo or mono FLAC becomes
// one AAC track, the default; surround FLAC becomes the AAC 2.0 default
// first and an AC-3 5.1 encoded from it second.
func TestFLACBecomesTheAACDefaultWithAnAC3CompanionForSurround(t *testing.T) {
	aac := AudioPlan{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true}
	assert.Equal(t, []AudioPlan{aac}, Plan(info(hevc10, audio(0, "flac", 2, "stereo", "eng")), profile, cpu).Audio)
	for _, layout := range []struct {
		ch   int32
		name string
	}{{6, "5.1(side)"}, {8, "7.1"}} {
		assert.Equal(t, []AudioPlan{
			aac,
			{SourceIndex: 0, Action: "ac3", Channels: 6, Layout: "5.1", BitRate: 640000, Language: "eng", Title: "Dolby Digital 5.1"},
		}, Plan(info(hevc10, audio(0, "flac", layout.ch, layout.name, "eng")), profile, cpu).Audio, layout.name)
	}
}

func TestHEVC8BitIsCompliantOnlyAtTheEightBitTarget(t *testing.T) {
	hevc8 := video("hevc", "yuv420p", 8, commonv1.HdrFormatNone)
	stereo := audio(0, "aac", 2, "stereo", "eng")
	assert.Equal(t, DecisionSkip, Plan(mp4Info(hevc8, stereo), profile, cpu).Decision, "1080p SDR HEVC Main is the standard")
	hevc8.Width, hevc8.Height = 3840, 2160
	p := Plan(mp4Info(hevc8, stereo), profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, "above 1080p the standard is Main 10")
	assert.Equal(t, "main10", p.Video.Options["profile"])
	assert.Equal(t, "format=yuv420p10le", p.Video.Filter)
	assert.Equal(t, "yuv420p10le", p.Expect.PixelFormat)
}

// policy.keepCodecs with h264 keeps 8-bit SDR H.264 as it is: the copy path
// compliant HEVC takes -- an MKV remuxed into the MP4 layout and tagged, so
// final -- where the standard encodes it to HEVC (owner, 2026-10-15). A
// 10-bit (Hi10P) or HDR H.264 is still encoded, which many players, Apple's
// among them, cannot decode.
func TestKeepingH264CopiesEightBitSDRH264(t *testing.T) {
	keep := profile
	keep.KeepCodecs = []string{"hevc", "h264"}
	p := Plan(info(h264, eac3), keep, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, p.Reason)
	assert.Equal(t, "H.264 video copied into the MP4 layout (policy.keepCodecs); audio and subtitles processed", p.Reason)
	assert.Equal(t, "copy", p.Video.Action)
	assert.Equal(t, "h264", p.Expect.VideoCodec, "the output is verified as the H.264 it is")
	assert.Equal(t, "yuv420p", p.Expect.PixelFormat)
	assert.Equal(t, transcode.ContainerMP4, p.Container)
	assert.Equal(t, "hevc-mkv@abc", p.Tags["CLUSTARR_PROFILE"])
	assert.Equal(t, []AudioPlan{
		{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true},
		{SourceIndex: 0, Action: "copy", Language: "eng"},
	}, p.Audio, "the audio takes the standard's layout as an encode's does")
	assert.Equal(t, DecisionSkip, Plan(mp4Info(h264, audio(0, "aac", 2, "stereo", "eng")), keep, cpu).Decision,
		"already in the MP4 layout")

	hi10p := video("h264", "yuv420p10le", 10, commonv1.HdrFormatNone)
	assert.Equal(t, DecisionEncode, Plan(info(hi10p, eac3), keep, cpu).Decision, "Hi10P is still encoded")
	hdr := video("h264", "yuv420p", 8, commonv1.HdrFormatHDR10)
	assert.Equal(t, DecisionEncode, Plan(info(hdr, eac3), keep, cpu).Decision, "HDR H.264 is still encoded")
	dv9 := video("h264", "yuv420p", 8, commonv1.HdrFormatDolbyVisionSDR)
	assert.Equal(t, DecisionEncode, Plan(info(dv9, eac3), keep, cpu).Decision,
		"Dolby Vision is stripped by an encode, as it is from HEVC, not copied")
	assert.Equal(t, DecisionEncode, Plan(info(h264, eac3), profile, cpu).Decision, "without h264 listed, it is encoded")
}

// A file clustarr remuxed keeping its 8-bit SDR H.264 (policy.keepCodecs)
// is encoded once the profile no longer keeps H.264 (owner, 2026-10-19:
// 5,956 such files, 12.6 TB, remuxed before the owner dropped it); while
// the profile keeps it, the remux is final as every transcode is. Only
// H.264 a remux kept: clustarr's HEVC is never encoded again.
func TestARemuxThatKeptH264IsEncodedOnceH264IsNoLongerKept(t *testing.T) {
	remuxed := mp4Info(h264, audio(0, "aac", 2, "stereo", "eng"))
	remuxed.Tags = map[string]string{"CLUSTARR_PROFILE": "hevc-mkv@old"}
	keep := profile
	keep.KeepCodecs = []string{"hevc", "h264"}
	assert.Equal(t, DecisionSkip, Plan(remuxed, keep, cpu).Decision, "kept, the remux is final")

	p := Plan(remuxed, profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	assert.Equal(t, "hevc-mkv@abc", p.Tags["CLUSTARR_PROFILE"], "tagged anew")

	own := mp4Info(hevc10, audio(0, "aac", 2, "stereo", "eng"))
	own.Tags = map[string]string{"CLUSTARR_PROFILE": "hevc-mkv@old"}
	assert.Equal(t, DecisionSkip, Plan(own, profile, cpu).Decision, "clustarr's HEVC stays final")
}

// A source another tool transcoded -- an FFmpeg HEVC or AV1 encode, Tdarr's
// QSV MKVs on the owner's library -- is final: it is remuxed into the MP4
// layout when its video is compliant, and never encoded again (owner,
// 2026-10-15). An FFmpeg H.264 encode is a release format, not one.
func TestATranscodedElsewhereSourceIsRemuxedNeverEncoded(t *testing.T) {
	tdarr := hevc10
	tdarr.Encoder = "Lavc61.3.100 hevc_qsv"
	p := Plan(info(tdarr, eac3), profile, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, p.Reason)

	uhd8 := video("hevc", "yuv420p", 8, commonv1.HdrFormatNone)
	uhd8.Width, uhd8.Height, uhd8.Encoder = 3840, 2160, "Lavc61.3.100 hevc_qsv"
	p = Plan(info(uhd8, eac3), profile, cpu)
	require.Equal(t, DecisionSkip, p.Decision, "8-bit HEVC above 1080p is encoded -- but not a second time")
	assert.Contains(t, p.Reason, "transcoded elsewhere")

	x264 := h264
	x264.Encoder = "Lavc61.11.100 libx264"
	assert.Equal(t, DecisionEncode, Plan(info(x264, eac3), profile, cpu).Decision, "an FFmpeg H.264 encode is not a transcode")
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
		{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true},
		{SourceIndex: 0, Action: "copy", Language: "eng"},
	}, p.Audio, "eng: the E-AC-3 is primary, the FLAC and DTS mixes dropped; fre AC-3 and the French commentary dropped by languages")
	// Commentary stays only in a kept language (the owner, 2026-10-15;
	// before, commentary in every language was kept).
	assert.Contains(t, p.Dropped, "audio 4 (fre): not a kept language")
}

func TestLanguagesNeverLeaveAFileSilent(t *testing.T) {
	p := Plan(info(h264, audio(0, "ac3", 6, "5.1", "jpn")), Profile{Name: "p", Hash: "h", Quality: 24, Languages: []string{"eng"}}, cpu)
	require.NotEmpty(t, p.Audio, "no track matches eng: keep them all rather than write a silent file")
	assert.Equal(t, "jpn", p.Audio[0].Language)
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

// An encoded track's layout is canonical for what it becomes: AAC mono or
// stereo, or AC-3 "5.1" (back surrounds) for any surround source; the
// resampler maps the source's positions onto it.
func TestEncodedLayoutsAreCanonical(t *testing.T) {
	for _, c := range []struct {
		ch      int32
		layouts []string
	}{{1, []string{"mono"}}, {2, []string{"stereo"}}, {3, []string{"stereo", "5.1"}}, {6, []string{"stereo", "5.1"}}, {8, []string{"stereo", "5.1"}}} {
		p := Plan(info(h264, audio(0, "flac", c.ch, "whatever(side)", "eng")), profile, cpu)
		var got []string
		for _, a := range p.Audio {
			got = append(got, a.Layout)
		}
		assert.Equal(t, c.layouts, got, "%d channels", c.ch)
	}
}

// The profile's container is ignored since Version 2: every plan is MP4.
func TestTheProfilesContainerIsIgnored(t *testing.T) {
	mp4 := profile
	mp4.Container = transcode.ContainerMP4
	in := info(h264, eac3)
	assert.Equal(t, Plan(in, profile, cpu).Hash(), Plan(in, mp4, cpu).Hash())
}

// A transcoded file is final (spec §5): one this profile wrote is skipped,
// and so is one written under an earlier hash (a profile edit, a new
// standard.Version) or by another profile -- re-transcoding is never an
// upgrade side effect. (The one exception, H.264 a remux kept, is
// TestARemuxThatKeptH264IsEncodedOnceH264IsNoLongerKept's.)
func TestATranscodedFileIsSkippedWhateverItsTag(t *testing.T) {
	uhd8 := video("hevc", "yuv420p", 8, commonv1.HdrFormatNone)
	uhd8.Width, uhd8.Height = 3840, 2160
	in := info(uhd8, audio(0, "truehd", 8, "7.1", "eng")) // an MKV, whose video the standard encodes to Main 10
	assert.Equal(t, DecisionEncode, Plan(in, profile, cpu).Decision, "untagged, it is encoded")
	for _, tag := range []string{profile.Name + "@" + profile.Hash, profile.Name + "@older", "other@" + profile.Hash} {
		in.Tags = map[string]string{"CLUSTARR_PROFILE": tag}
		p := Plan(in, profile, cpu)
		assert.Equal(t, DecisionSkip, p.Decision, tag)
		assert.Contains(t, p.Reason, "already transcoded", tag)
	}
}

// clustarr's own transcodes from before the MP4 layout (HEVC in Matroska,
// standard.Version 1) are remuxed into it, whatever profile or hash tagged
// them: the video copied, the audio and subtitles given the layout, and
// never encoded a second time (owner, 2026-10-15; the H.264 case is
// TestATranscodedFileIsSkippedWhateverItsTag). One in an MP4 is final.
func TestClustarrsOwnMKVTranscodeIsRemuxedIntoTheMP4Layout(t *testing.T) {
	in := info(hevc10, audio(0, "aac", 6, "5.1", "eng"))
	in.Tags = map[string]string{"CLUSTARR_PROFILE": "hevc-mkv@older"}
	p := Plan(in, profile, cpu)
	require.Equal(t, DecisionCopyVideo, p.Decision, p.Reason)
	assert.Equal(t, "copy", p.Video.Action)
	assert.Equal(t, transcode.ContainerMP4, p.Container)
	assert.Equal(t, "hevc-mkv@abc", p.Tags["CLUSTARR_PROFILE"], "tagged anew, so final once remuxed")
	assert.Equal(t, "clustarr's own Matroska transcode remuxed into the MP4 layout: its video copied, never encoded again", p.Reason)
	var actions []string
	for _, a := range p.Audio {
		actions = append(actions, a.Action)
	}
	assert.Equal(t, []string{AudioAAC, AudioAC3}, actions, "AAC 2.0 first, then the surround track as AC-3")
	require.NotEmpty(t, p.Sidecars)
	assert.Equal(t, "en.srt", p.Sidecars[0].Suffix)

	mp4 := mp4Info(hevc10, audio(0, "aac", 6, "5.1", "eng"))
	mp4.Tags = in.Tags
	p = Plan(mp4, profile, cpu)
	assert.Equal(t, DecisionSkip, p.Decision, "an MP4 transcode is final, its audio the layout's or not")
	assert.Contains(t, p.Reason, "already transcoded")
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

// NVENC holds its output under the source (the old engine's
// video.nvenc.maxBitratePercent, restored 2026-10-05): constant QP alone
// made an already lean source larger -- five jobs on kind-cluster-plex
// failed policy.maxOutputToSourcePercent at up to 133%. With the source's
// video bit rate known, NVENC runs VBR at the quality's cq, capped at 70% of
// it; unknown, it stays constant QP.
func TestNVENCCapsItsBitRateBelowTheSources(t *testing.T) {
	src := h264
	src.BitRateKbps = 4000
	o := Plan(info(src, eac3), profile, nvenc).Video.Options
	assert.Equal(t, "vbr", o["rc"])
	assert.Equal(t, "23", o["cq"], "quality 24, as NVENC's qp is quality - 1")
	assert.Equal(t, "0", o["b"], "no target bit rate: cq decides, maxrate caps")
	assert.Equal(t, "2800k", o["maxrate"])
	assert.Equal(t, "5600k", o["bufsize"])
	assert.NotContains(t, o, "qp")

	unknown := Plan(info(h264, eac3), profile, nvenc).Video.Options
	assert.Equal(t, "constqp", unknown["rc"])
	assert.Equal(t, "23", unknown["qp"])
	assert.NotContains(t, unknown, "maxrate")
}

func aud(codec string, ch int32, lang, title string) transcode.AudioStream {
	return transcode.AudioStream{Codec: codec, Channels: ch, Language: lang, Title: title}
}

func TestPlanAudioLayout(t *testing.T) {
	type out struct {
		src     int32
		action  string
		ch      int32
		def     bool
		title   string
		comment bool
	}
	for _, tc := range []struct {
		name string
		in   []transcode.AudioStream
		want []out
	}{
		{
			"E-AC-3 5.1 is copied and gains an AAC 2.0 companion",
			[]transcode.AudioStream{aud("eac3", 6, "en", "English")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {0, "copy", 0, false, "English", false}},
		},
		{
			"E-AC-3 7.1 Atmos is copied as is",
			[]transcode.AudioStream{aud("eac3", 8, "en", "Atmos")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {0, "copy", 0, false, "Atmos", false}},
		},
		{
			"DTS-HD 5.1 becomes AC-3 5.1 plus AAC 2.0",
			[]transcode.AudioStream{aud("dts", 6, "en", "DTS-HD MA 5.1")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {0, "ac3", 6, false, "Dolby Digital 5.1", false}},
		},
		{
			"TrueHD 7.1 beside an AC-3 5.1 core: the AC-3 is copied, the TrueHD dropped",
			[]transcode.AudioStream{aud("truehd", 8, "en", "TrueHD Atmos"), aud("ac3", 6, "en", "AC-3")},
			[]out{{1, "aac", 2, true, "Stereo", false}, {1, "copy", 0, false, "AC-3", false}},
		},
		{
			"E-AC-3 wins over AC-3 in the same language",
			[]transcode.AudioStream{aud("ac3", 6, "en", ""), aud("eac3", 6, "en", "")},
			[]out{{1, "aac", 2, true, "Stereo", false}, {1, "copy", 0, false, "", false}},
		},
		{
			"multichannel AAC becomes AC-3 plus AAC 2.0",
			[]transcode.AudioStream{aud("aac", 6, "ja", "")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {0, "ac3", 6, false, "Dolby Digital 5.1", false}},
		},
		{
			"stereo AAC is copied alone",
			[]transcode.AudioStream{aud("aac", 2, "en", "Stereo")},
			[]out{{0, "copy", 0, true, "Stereo", false}},
		},
		{
			"stereo AC-3 becomes AAC alone",
			[]transcode.AudioStream{aud("ac3", 2, "en", "")},
			[]out{{0, "aac", 2, true, "Stereo", false}},
		},
		{
			"mono FLAC becomes AAC mono",
			[]transcode.AudioStream{aud("flac", 1, "en", "")},
			[]out{{0, "aac", 1, true, "Mono", false}},
		},
		{
			"a second English mix is dropped; commentary is kept as AAC 2.0",
			[]transcode.AudioStream{aud("eac3", 6, "en", ""), aud("aac", 2, "en", "Stereo"), aud("ac3", 6, "en", "Director's Commentary")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {0, "copy", 0, false, "", false}, {2, "aac", 2, false, "Director's Commentary", true}},
		},
		{
			"languages keep their order; each gets its own layout",
			[]transcode.AudioStream{aud("flac", 2, "ja", ""), aud("eac3", 6, "en", "")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {1, "aac", 2, false, "Stereo", false}, {1, "copy", 0, false, "", false}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := planAudio(tc.in, wanted{all: true})
			require.Len(t, got, len(tc.want))
			for i, w := range tc.want {
				g := got[i]
				assert.Equal(t, w.src, g.SourceIndex, "track %d source", i)
				assert.Equal(t, w.action, g.Action, "track %d action", i)
				if w.ch != 0 {
					assert.Equal(t, w.ch, g.Channels, "track %d channels", i)
				}
				assert.Equal(t, w.def, g.Default, "track %d default", i)
				assert.Equal(t, w.title, g.Title, "track %d title", i)
				assert.Equal(t, w.comment, g.Comment, "track %d comment", i)
			}
		})
	}
}

// Ruling R4: the source's default-flagged language keeps the default.
func TestPlanAudioKeepsTheSourcesDefaultLanguage(t *testing.T) {
	en, ja := aud("aac", 2, "en", ""), aud("aac", 2, "ja", "")
	ja.Disposition.Default = true
	got, _ := planAudio([]transcode.AudioStream{en, ja}, wanted{all: true})
	require.Len(t, got, 2)
	assert.False(t, got[0].Default)
	assert.True(t, got[1].Default)
}

func TestTheBitRatesAndLayouts(t *testing.T) {
	got, _ := planAudio([]transcode.AudioStream{aud("dts", 8, "en", "")}, wanted{all: true})
	require.Len(t, got, 2)
	assert.Equal(t, int64(160000), got[0].BitRate)
	assert.Equal(t, "stereo", got[0].Layout)
	assert.Equal(t, int64(640000), got[1].BitRate)
	assert.Equal(t, "5.1", got[1].Layout)
}

func TestPlanAlwaysWritesMP4(t *testing.T) {
	p := Plan(info(h264, eac3), Profile{Name: "p", Hash: "h", Container: transcode.ContainerMKV}, cpu)
	assert.Equal(t, transcode.ContainerMP4, p.Container)
	assert.False(t, p.Attachments)
}

func TestVersionIsSix(t *testing.T) { assert.Equal(t, 6, Version) }

func sub(codec, lang, title string, forced, hi bool) transcode.SubtitleStream {
	return transcode.SubtitleStream{
		Codec: codec, Language: lang, Title: title,
		Disposition: transcode.Disposition{Forced: forced, HearingImpaired: hi},
	}
}

func TestPlanSubtitles(t *testing.T) {
	sidecars, dropped := planSubtitles([]transcode.SubtitleStream{
		sub("subrip", "eng", "", false, false),          // 0: en.srt (639-2/B to 639-1)
		sub("subrip", "en", "SDH", false, true),         // 1: en.sdh.srt
		sub("webvtt", "es", "", false, false),           // 2: es.srt
		sub("mov_text", "fre", "", false, false),        // 3: fr.srt
		sub("ass", "en", "Full Subs", false, false),     // 4: dropped, en.srt taken
		sub("ass", "en", "Signs & Songs", false, false), // 5: en.forced.srt (title, R2), from ASS
		sub("subrip", "ger", "Forced", true, false),     // 6: de.forced.srt (R2)
		sub("ass", "ja", "Honorifics", false, false),    // 7: ja.srt, from ASS
		sub("ass", "", "", false, false),                // 8: dropped, srt taken by 11
		sub("eia_608", "en", "", false, false),          // 9: dropped, unknown text codec
		sub("subrip", "pt-BR", "", false, false),        // 10: pt.srt (the region dropped)
		sub("subrip", "und", "", false, false),          // 11: srt (no language)
		sub("ssa", "it", "", false, false),              // 12: it.srt, from SSA
	}, wanted{all: true})
	assert.Equal(t, []SidecarPlan{
		{SourceIndex: 0, Format: SidecarSRT, Codec: "subrip", Suffix: "en.srt"},
		{SourceIndex: 1, Format: SidecarSRT, Codec: "subrip", Suffix: "en.sdh.srt"},
		{SourceIndex: 2, Format: SidecarSRT, Codec: "webvtt", Suffix: "es.srt"},
		{SourceIndex: 3, Format: SidecarSRT, Codec: "mov_text", Suffix: "fr.srt"},
		{SourceIndex: 5, Format: SidecarSRT, Codec: "ass", Suffix: "en.forced.srt"},
		{SourceIndex: 6, Format: SidecarSRT, Codec: "subrip", Suffix: "de.forced.srt"},
		{SourceIndex: 7, Format: SidecarSRT, Codec: "ass", Suffix: "ja.srt"},
		{SourceIndex: 8, Format: SidecarSRT, Codec: "ass", Suffix: "srt"},
		{SourceIndex: 10, Format: SidecarSRT, Codec: "subrip", Suffix: "pt.srt"},
		{SourceIndex: 12, Format: SidecarSRT, Codec: "ssa", Suffix: "it.srt"},
	}, sidecars)
	require.Len(t, dropped, 3)
	assert.Contains(t, dropped[0], "subtitle 4")
	assert.Contains(t, dropped[1], "subtitle 9")
	assert.Contains(t, dropped[2], "subtitle 11")
}

// An ASS track becomes a SubRip sidecar like any text track: Plex's web
// player shows an ASS subtitle only by burning it into the video, a video
// transcode cluster-plex never allows (talos, 2026-10-20).
func TestAnSDHAssTrackIsNamedSDH(t *testing.T) {
	sidecars, _ := planSubtitles([]transcode.SubtitleStream{sub("ass", "en", "", false, true)}, wanted{all: true})
	require.Len(t, sidecars, 1)
	assert.Equal(t, SidecarPlan{Format: SidecarSRT, Codec: "ass", Suffix: "en.sdh.srt"}, sidecars[0])
}

func TestAnImageSubtitleNoTextTrackCoversHoldsTheFile(t *testing.T) {
	in := info(h264, eac3)
	in.Subtitles = []transcode.SubtitleStream{
		{Index: 0, Codec: "subrip", Language: "spa"},
		{Index: 1, Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: "eng"},
	}
	p := Plan(in, profile, cpu)
	assert.Equal(t, DecisionSkip, p.Decision)
	assert.Equal(t, HoldImageSubtitles, p.Reason, "only a Spanish text track: the English image one waits for OCR")
}

// Version 6 (owner, 2026-10-20): an image subtitle a text subtitle of its
// language covers is dropped rather than holding the file for OCR. Akira
// held for its English DVD track beside three English ASS ones.
func TestAnImageSubtitleATextTrackOfItsLanguageCoversIsDropped(t *testing.T) {
	akira := info(video("h264", "yuv420p10le", 10, commonv1.HdrFormatNone), audio(0, "flac", 6, "5.1", "jpn"))
	akira.Subtitles = []transcode.SubtitleStream{
		{Index: 4, Codec: "ass", Language: "eng"},
		{Index: 5, Codec: "ass", Language: "eng", Title: "niizk"},
		{Index: 6, Codec: "ass", Language: "eng", Title: "THORA"},
		{Index: 7, Codec: "dvd_subtitle", Bitmap: true, Language: "eng"},
	}
	p := Plan(akira, profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	require.Len(t, p.Sidecars, 1)
	assert.Equal(t, "en.srt", p.Sidecars[0].Suffix)
	assert.Equal(t, int32(0), p.Sidecars[0].SourceIndex, "the first English text track")
	require.Len(t, p.Dropped, 3)
	assert.Contains(t, p.Dropped[2], "subtitle 3 (dvd_subtitle")
	assert.Contains(t, p.Dropped[2], "an image subtitle an en text track covers")
}

func TestATextTrackCoversAnImageSubtitleOnlyInItsOwnLanguageAndKind(t *testing.T) {
	img := func(lang string, forced bool) transcode.SubtitleStream {
		return transcode.SubtitleStream{Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: lang, Disposition: transcode.Disposition{Forced: forced}}
	}
	for name, tc := range map[string]struct {
		subs []transcode.SubtitleStream
		held bool
	}{
		"same language, 639-2 beside 639-1": {[]transcode.SubtitleStream{sub("subrip", "en", "", false, false), img("eng", false)}, false},
		"an SDH text track covers":          {[]transcode.SubtitleStream{sub("subrip", "eng", "SDH", false, true), img("eng", false)}, false},
		"another language does not":         {[]transcode.SubtitleStream{sub("subrip", "spa", "", false, false), img("eng", false)}, true},
		// A forced track carries only what the audio does not say: a full
		// one in its place would show every line.
		"a full track does not cover a forced one": {[]transcode.SubtitleStream{sub("subrip", "eng", "", false, false), img("eng", true)}, true},
		"a forced title is forced":                 {[]transcode.SubtitleStream{sub("subrip", "eng", "", false, false), {Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: "eng", Title: "Forced"}}, true},
		"a forced track covers a forced one":       {[]transcode.SubtitleStream{sub("subrip", "eng", "Forced", true, false), img("eng", true)}, false},
		"a forced track does not cover a full one": {[]transcode.SubtitleStream{sub("subrip", "eng", "Forced", true, false), img("eng", false)}, true},
		// An untagged track's language is unknown on both sides.
		"an untagged image track holds":               {[]transcode.SubtitleStream{sub("subrip", "", "", false, false), img("", false)}, true},
		"an untagged text track covers none":          {[]transcode.SubtitleStream{sub("subrip", "", "", false, false), img("eng", false)}, true},
		"a text codec the standard drops covers none": {[]transcode.SubtitleStream{sub("dvb_teletext", "eng", "", false, false), img("eng", false)}, true},
		"another image track covers none":             {[]transcode.SubtitleStream{img("eng", false), img("eng", false)}, true},
		"every image track must be covered":           {[]transcode.SubtitleStream{sub("subrip", "eng", "", false, false), img("eng", false), img("fre", false)}, true},
	} {
		t.Run(name, func(t *testing.T) {
			in := info(h264, eac3)
			in.Subtitles = tc.subs
			p := Plan(in, profile, cpu)
			if tc.held {
				assert.Equal(t, HoldImageSubtitles, p.Reason)
			} else {
				assert.NotEqual(t, DecisionSkip, p.Decision, p.Reason)
			}
		})
	}
}

// Every subtitle goes beside the file (spec §4.1): the MP4 carries none.
func TestTheMP4CarriesNoSubtitleStream(t *testing.T) {
	p := Plan(info(h264, eac3), profile, cpu) // subrip and ass, both English
	assert.Equal(t, int32(0), p.Expect.SubtitleStreams)
	assert.Empty(t, p.Subtitles)
	require.Len(t, p.Sidecars, 1, "the ASS track's en.srt is the SubRip track's")
	assert.Equal(t, "en.srt", p.Sidecars[0].Suffix)
}

// An MP4 already in the layout with no subtitle stream is left alone; one
// carrying mov_text is remuxed, the text moved beside it as SubRip.
func TestTheSubtitleLayoutDecidesTheSkip(t *testing.T) {
	stereo := audio(0, "aac", 2, "stereo", "eng")
	in := mp4Info(hevc10, stereo)
	assert.Equal(t, DecisionSkip, Plan(in, profile, cpu).Decision)
	in.Subtitles = []transcode.SubtitleStream{sub("mov_text", "eng", "", false, false)}
	assert.Equal(t, DecisionCopyVideo, Plan(in, profile, cpu).Decision)
}

// Older Matroska files carry no forced or hearing-impaired flag, only a
// title; and a full track whose title mentions signs is still full (final
// review I5).
func TestSubtitleFlagsAreReadFromTheTitleToo(t *testing.T) {
	sidecars, dropped := planSubtitles([]transcode.SubtitleStream{
		sub("subrip", "en", "English", false, false),
		sub("subrip", "en", "English (SDH)", false, false),
		sub("subrip", "en", "Forced", false, false),
		sub("ass", "en", "Dialogue + Signs & Songs", false, false),
		sub("ass", "en", "Signs & Songs", false, false),
		sub("subrip", "de", "Deutsch HI", false, false),
		sub("subrip", "fr", "CC", false, false),
		sub("subrip", "es", "Accented", false, false), // "cc" inside a word is no flag
	}, wanted{all: true})
	var suffixes []string
	for _, s := range sidecars {
		suffixes = append(suffixes, s.Suffix)
	}
	assert.Equal(t, []string{"en.srt", "en.sdh.srt", "en.forced.srt", "de.sdh.srt", "fr.sdh.srt", "es.srt"}, suffixes)
	require.Len(t, dropped, 2, "the ASS tracks' names are the SubRip tracks'")
}
