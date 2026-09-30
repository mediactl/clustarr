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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// goldenPath resolves name to its golden fixture, relative to the package
// directory (Go tests run with the package directory as CWD), so the
// repo-root test/data/transcode/ tree is reached via ../../.
func goldenPath(name string) string {
	return filepath.Join("..", "..", "test", "data", "transcode", "golden", name+".golden")
}

func assertGolden(t *testing.T, name string, got []string) {
	t.Helper()
	joined := strings.Join(got, "\n") + "\n"
	path := goldenPath(name)
	if updateGolden {
		require.NoError(t, os.WriteFile(path, []byte(joined), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file %s (run with UPDATE_GOLDEN=1 once, then hand-verify against the cited note section)", path)
	require.Equal(t, string(want), joined)
}

func fps24() transcode.Rational { return transcode.Rational{Num: 24, Den: 1} }

var testCaps = transcode.Capabilities{Encoders: map[transcode.Tier]bool{
	transcode.TierCPUx265: true, transcode.TierNVENC: true, transcode.TierQSV: true, transcode.TierVAAPI: true,
}}
var testMeta = transcode.PlanMeta{ProfileName: "hevc10-aac-space", ProfileHash: "abc12345", Threads: 8}

func TestArgsGoldenSDR1080pH264CPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: fps24(),
			Disposition: transcode.Disposition{Default: true},
		}},
		Audio: []transcode.AudioStream{{
			Codec: "ac3", Channels: 6, ChannelLayout: "5.1", Language: "eng",
			Disposition: transcode.Disposition{Default: true},
		}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierCPUx265, plan.Tier)

	assertGolden(t, "sdr_1080p_h264_cpu", transcode.Args(plan))
}

// TestArgsGoldenHDR102160pCPU is transcribed against docs/research/transcode.md
// §3.4's reference command: the -vf setparams filter and the x265-params
// hdr10/master-display/max-cll block.
func TestArgsGoldenHDR102160pCPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FrameRate: fps24(),
			ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc", ColorRange: "tv",
			HDR: transcode.HDRInfo{
				Format: commonv1.HdrFormatHDR10,
				MasteringDisplay: &mediainfo.MasteringDisplay{
					GreenX: 13250, GreenY: 34500,
					BlueX: 7500, BlueY: 3000,
					RedX: 34000, RedY: 16000,
					WhiteX: 15635, WhiteY: 16450,
					MaxLuminance: 10000000, MinLuminance: 1,
				},
				ContentLight: &mediainfo.ContentLight{MaxCLL: 1000, MaxFALL: 400},
			},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierCPUx265, plan.Tier)

	assertGolden(t, "hdr10_2160p_cpu", transcode.Args(plan))
}

// TestArgsGoldenDolbyVisionP5PassthroughCPU is transcribed against
// docs/research/transcode.md §3.5: Dolby Vision always forces cpu-x265
// (here proven by asking for nvidia hardware and getting cpu-x265 anyway),
// requires VBV (-maxrate/-bufsize) and omits the HDR10 static-metadata
// block, since profile 5 carries no base-layer HDR10 tags of its own.
func TestArgsGoldenDolbyVisionP5PassthroughCPU(t *testing.T) {
	maxRate := int32(40000)
	bufSize := int32(60000)
	profile := defaultProfile()
	profile.Hardware = transcode.HardwareNVIDIA
	profile.Video.MaxRateKbps = &maxRate
	profile.Video.BufSizeKbps = &bufSize

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FrameRate: fps24(),
			ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc", ColorRange: "tv",
			HDR: transcode.HDRInfo{
				Format:      commonv1.HdrFormatDolbyVision,
				DolbyVision: &mediainfo.DoviRecord{Profile: 5, BLSignalCompatibilityID: 0, RPUPresent: true, BLPresent: true},
			},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierCPUx265, plan.Tier, "Dolby Vision forces cpu-x265 even though the profile asked for nvidia")

	assertGolden(t, "dolbyvision_p5_passthrough_cpu", transcode.Args(plan))
}

// exampleSDRVideo returns the SDR 1080p h264 video stream shared by several
// of the remaining golden cases below.
func exampleSDRVideo() transcode.VideoStream {
	return transcode.VideoStream{
		Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: fps24(),
	}
}

// TestArgsGoldenAlreadyHEVC10Skip: note §2.4, a source already encoded to
// the profile's exact target never invokes ffmpeg.
func TestArgsGoldenAlreadyHEVC10Skip(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le",
			Width: 1920, Height: 1080, FrameRate: fps24(),
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionSkip, plan.Decision)
	require.Nil(t, transcode.Args(plan))

	assertGolden(t, "already_hevc10_skip", transcode.Args(plan))
}

// TestArgsGoldenMultiAudioAtmosCPU: note §5.2 point 3, KeepOriginal=atmos
// doubles an Atmos-flagged track into an AAC encode plus a stream-copy of
// the original, both keyed off the same source index.
func TestArgsGoldenMultiAudioAtmosCPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{exampleSDRVideo()},
		Audio: []transcode.AudioStream{{
			Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8, ChannelLayout: "7.1",
			Language: "eng", Lossless: true, Atmos: true,
			Disposition: transcode.Disposition{Default: true},
		}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, []transcode.AudioTrackPlan{
		{SourceIndex: 0, Action: transcode.AudioActionEncode, Codec: "aac", BitrateKbps: 512, Language: "eng", Default: true},
		{SourceIndex: 0, Action: transcode.AudioActionCopy, Codec: "copy", Language: "eng"},
	}, plan.Audio)

	assertGolden(t, "multi_audio_atmos_cpu", transcode.Args(plan))
}

// TestArgsGoldenMultiAudioAtmosAACCPU: controller ruling -- every source
// audio track gets its own AudioTrackPlan under buildAudioPlan's documented
// rules (one encode entry per kept track, plus a KeepOriginal duplicate
// when that track's policy condition matches). Two source tracks: an
// Atmos-flagged truehd track (doubled into an AAC encode plus a
// stream-copy passthrough, same as the single-track case) and a second,
// plain aac stereo track that is neither Atmos nor lossless, so it gets
// only its own AAC re-encode with no KeepOriginal duplicate.
func TestArgsGoldenMultiAudioAtmosAACCPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{exampleSDRVideo()},
		Audio: []transcode.AudioStream{
			{
				Index: 0, Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8, ChannelLayout: "7.1",
				Language: "eng", Lossless: true, Atmos: true,
				Disposition: transcode.Disposition{Default: true},
			},
			{
				Index: 1, Codec: "aac", Channels: 2, ChannelLayout: "stereo",
				Language: "eng",
			},
		},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, []transcode.AudioTrackPlan{
		{SourceIndex: 0, Action: transcode.AudioActionEncode, Codec: "aac", BitrateKbps: 512, Language: "eng", Default: true},
		{SourceIndex: 0, Action: transcode.AudioActionCopy, Codec: "copy", Language: "eng"},
		{SourceIndex: 1, Action: transcode.AudioActionEncode, Codec: "aac", BitrateKbps: 128, Language: "eng"},
	}, plan.Audio)

	assertGolden(t, "multi_audio_atmos_aac_cpu", transcode.Args(plan))
}

// TestArgsGoldenForcedSubsCPU: note §6, every kept text subtitle stream is
// mapped and stream-copied, forced and non-forced alike.
func TestArgsGoldenForcedSubsCPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{exampleSDRVideo()},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
		Subtitles: []transcode.SubtitleStream{
			{Index: 4, Codec: "subrip", Language: "eng"},
			{Index: 5, Codec: "subrip", Language: "eng", Disposition: transcode.Disposition{Forced: true}},
		},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, []int32{4, 5}, plan.Subtitles)
	require.Equal(t, int32(1+1+2), plan.Expect.Streams)

	assertGolden(t, "forced_subs_cpu", transcode.Args(plan))
}

// TestArgsGoldenRemuxOnlyAudioCPU: note §2.4, an already hevc/main10/
// yuv420p10le video is stream-copied even when the audio (here lossless
// DTS-HD MA, but not Atmos, so no duplicate under the default "atmos"
// keep-original policy) still needs the full audio pipeline.
func TestArgsGoldenRemuxOnlyAudioCPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le",
			Width: 1920, Height: 1080, FrameRate: fps24(),
		}},
		Audio: []transcode.AudioStream{{
			Codec: "dts", Profile: "DTS-HD MA", Channels: 6, ChannelLayout: "5.1",
			Language: "eng", Lossless: true, Disposition: transcode.Disposition{Default: true},
		}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionRemuxOnly, plan.Decision)
	require.Equal(t, []string{"-c:v", "copy"}, plan.VideoArgs)

	assertGolden(t, "remux_only_audio_cpu", transcode.Args(plan))
}

func hdr10Fixture() transcode.HDRInfo {
	return transcode.HDRInfo{
		Format: commonv1.HdrFormatHDR10,
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

func hdr2160pVideo(hdr transcode.HDRInfo) transcode.VideoStream {
	return transcode.VideoStream{
		Codec: "h264", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FrameRate: fps24(),
		ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc", ColorRange: "tv",
		HDR: hdr,
	}
}

// TestArgsGoldenNVENCTierHDR102160p: note §4.1, the NVENC video-args block
// (NVENCSpec's CRD default values) with the same HDR10 filter/handling as
// the cpu-x265 case but no -x265-params (nvenc has none).
func TestArgsGoldenNVENCTierHDR102160p(t *testing.T) {
	profile := defaultProfile()
	profile.Hardware = transcode.HardwareNVIDIA
	profile.Video.NVENC = transcode.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"}

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{hdr2160pVideo(hdr10Fixture())},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierNVENC, plan.Tier)
	require.Empty(t, plan.HWInit)

	assertGolden(t, "nvenc_tier_hdr10_2160p", transcode.Args(plan))
}

// TestArgsGoldenQSVTierSDR1080p: note §4.2, QSV's hwaccel init block plus
// the QSVSpec video-args (CRD default values).
func TestArgsGoldenQSVTierSDR1080p(t *testing.T) {
	profile := defaultProfile()
	profile.Hardware = transcode.HardwareIntel
	profile.Video.QSV = transcode.QSVSpec{GlobalQuality: 22, Preset: "veryslow", LookAheadDepth: 40}

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{exampleSDRVideo()},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierQSV, plan.Tier)

	assertGolden(t, "qsv_tier_sdr_1080p", transcode.Args(plan))
}

// TestArgsGoldenVAAPITierFallbackSDR1080p: note §4.3, when the QSV encoder
// is absent from this node's ffmpeg build, FallbackTier drops to VAAPI, the
// vendor-neutral fallback on the same node.
func TestArgsGoldenVAAPITierFallbackSDR1080p(t *testing.T) {
	profile := defaultProfile()
	profile.Hardware = transcode.HardwareIntel

	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{
		transcode.TierCPUx265: true, transcode.TierVAAPI: true,
	}}

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{exampleSDRVideo()},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, profile, caps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierVAAPI, plan.Tier, "qsv unavailable falls back to vaapi")

	assertGolden(t, "vaapi_tier_fallback_sdr_1080p", transcode.Args(plan))
}

// TestArgsGoldenDolbyVisionP7DowngradeCPU: note §3.5, "profile 4 or 7 ...
// auto -> silently skip ... output is HDR10 base layer, RPU dropped" -- a
// dual-layer profile 7 source under hdr.dolbyVision=downgradeToHDR10 is
// rendered exactly like the plain HDR10 case (no -dolbyvision flag, no VBV
// requirement) using its own base-layer HDR10 static metadata.
func TestArgsGoldenDolbyVisionP7DowngradeCPU(t *testing.T) {
	profile := defaultProfile()
	profile.HDR.DolbyVision = transcode.DolbyVisionDowngradeToHDR10

	hdr := hdr10Fixture()
	hdr.Format = commonv1.HdrFormatDolbyVisionHDR10
	hdr.DolbyVision = &mediainfo.DoviRecord{Profile: 7, ELPresent: true, BLPresent: true, BLSignalCompatibilityID: 1}

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{hdr2160pVideo(hdr)},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	// No MaxRateKbps/BufSizeKbps: downgradeToHDR10 does not require VBV.
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Contains(t, plan.Reason, "profile 7 dual-layer Dolby Vision downgraded to HDR10")
	require.Equal(t, transcode.TierCPUx265, plan.Tier)

	assertGolden(t, "dolbyvision_p7_downgrade_cpu", transcode.Args(plan))
}

// TestArgsGoldenDolbyVisionRejectPolicy: hdr.dolbyVision=reject rejects
// every Dolby Vision source regardless of VBV settings.
func TestArgsGoldenDolbyVisionRejectPolicy(t *testing.T) {
	profile := defaultProfile()
	profile.HDR.DolbyVision = transcode.DolbyVisionReject

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FrameRate: fps24(),
			HDR: transcode.HDRInfo{Format: commonv1.HdrFormatDolbyVision, DolbyVision: &mediainfo.DoviRecord{Profile: 5}},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionReject, plan.Decision)
	require.Nil(t, transcode.Args(plan))

	assertGolden(t, "dolbyvision_reject_policy", transcode.Args(plan))
}

// TestArgsGoldenDolbyVisionP5MissingVBVReject: hdr.dolbyVision=passthrough
// (the default) rejects a Dolby Vision source when video.maxRateKbps/
// bufSizeKbps are unset.
func TestArgsGoldenDolbyVisionP5MissingVBVReject(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FrameRate: fps24(),
			HDR: transcode.HDRInfo{Format: commonv1.HdrFormatDolbyVision, DolbyVision: &mediainfo.DoviRecord{Profile: 5}},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionReject, plan.Decision)
	require.Contains(t, plan.Reason, "maxRateKbps")

	assertGolden(t, "dolbyvision_p5_missing_vbv_reject", transcode.Args(plan))
}

// TestArgsGoldenHDR10PlusDroppedCPU: note §3.6, HDR10+ dynamic metadata is
// simply never referenced; the static MDCV/CLL side data renders exactly
// like the plain HDR10 case.
func TestArgsGoldenHDR10PlusDroppedCPU(t *testing.T) {
	hdr := hdr10Fixture()
	hdr.Format = commonv1.HdrFormatHDR10Plus
	hdr.HasHDR10Plus = true

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{hdr2160pVideo(hdr)},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)

	assertGolden(t, "hdr10plus_dropped_cpu", transcode.Args(plan))
}

// TestArgsGoldenRemuxContainerMKVToMP4: note §6, remuxing an already
// compliant video/audio pair from mkv into mp4 tags the hevc stream hvc1,
// sets +faststart and +use_metadata_tags in one -movflags (without the
// second, the mp4 muxer drops the CLUSTARR_PROFILE tag), and drops the
// bitmap (PGS) subtitle mp4 cannot carry.
func TestArgsGoldenRemuxContainerMKVToMP4(t *testing.T) {
	profile := defaultProfile()
	profile.Container = transcode.ContainerMP4

	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le",
			Width: 1920, Height: 1080, FrameRate: fps24(),
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
		Subtitles: []transcode.SubtitleStream{
			{Index: 4, Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: "eng"},
		},
	}
	plan, err := transcode.Plan(info, profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionRemuxOnly, plan.Decision)
	require.Equal(t, []string{"-c:v", "copy", "-tag:v", "hvc1"}, plan.VideoArgs)
	require.Empty(t, plan.Subtitles, "mp4 cannot carry PGS subtitles")
	require.Equal(t, "/media/movies/Example (2019)/Example (2019).part.mp4", plan.Output)

	args := transcode.Args(plan)
	assertGolden(t, "remux_container_mkv_to_mp4", args)
	var movflags []string
	for i, a := range args {
		if a == "-movflags" && i+1 < len(args) {
			movflags = append(movflags, args[i+1])
		}
	}
	require.Equal(t, []string{"+faststart+use_metadata_tags"}, movflags,
		"one -movflags, both flags joined: use_metadata_tags is what keeps CLUSTARR_PROFILE in an mp4")
}
