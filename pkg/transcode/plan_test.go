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
)

// defaultProfile is the shared fixture the rest of this file's steps reuse.
func defaultProfile() transcode.ProfileSpec {
	return transcode.ProfileSpec{
		Container: transcode.ContainerMKV,
		Hardware:  transcode.HardwareCPU,
		Video: transcode.VideoSpec{
			Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10",
			CRF:          transcode.CRFTable{SD: 21, HD: 22, UHD: 23, HDROffset: -1},
			Preset:       "slow",
			KeyintFactor: 10, BFrames: 8, Refs: 4, RCLookahead: 40, AQMode: 3,
		},
		Audio: transcode.AudioSpec{
			Codec: "aac", BitratePerChannelKbps: 64,
			KeepOriginal: transcode.KeepOriginalAtmos, DropCommentary: true,
		},
		Subtitles: transcode.SubSpec{CopyText: true, CopyBitmap: true, CopyAttachments: true},
		HDR:       transcode.HDRSpec{HDR10Plus: transcode.HDR10PlusDrop, DolbyVision: transcode.DolbyVisionPassthrough},
		Policy: transcode.PolicySpec{
			SkipIfCompliant: true, RemuxOnlyWhenVideoCompliant: true,
			NeverTranscodeModifiers:  []string{"remux", "brdisk"},
			MinDuration:              time.Minute,
			MaxOutputToSourcePercent: 100,
			ReplaceSource:            true, RecycleBin: true,
		},
		Verify: transcode.VerifySpec{PacketCount: true},
	}
}

func TestProfileHashIsDeterministicAndSensitiveToChange(t *testing.T) {
	a := transcode.ProfileHash(defaultProfile())
	b := transcode.ProfileHash(defaultProfile())
	require.Equal(t, a, b, "hashing the same spec twice must be stable")

	changed := defaultProfile()
	changed.Video.CRF.HD = 21
	require.NotEqual(t, a, transcode.ProfileHash(changed), "a changed field must change the hash")
}

func TestCRFForSelectsResolutionClassAndHDROffset(t *testing.T) {
	table := transcode.CRFTable{SD: 21, HD: 22, UHD: 23, HDROffset: -1}
	cases := []struct {
		name   string
		height int32
		hdr    bool
		want   int32
	}{
		{"sd sdr", 480, false, 21},
		{"hd 1080p sdr", 1080, false, 22},
		{"hd 720p sdr", 720, false, 22},
		{"uhd 2160p sdr", 2160, false, 23},
		{"uhd 2160p hdr applies offset", 2160, true, 22},
		{"hd 1080p hdr applies offset", 1080, true, 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, transcode.CRFFor(table, tc.height, tc.hdr))
		})
	}
}

func TestPlanDecisionTable(t *testing.T) {
	profile := defaultProfile()

	compliantHEVC10 := transcode.MediaInfo{
		Path:   "/media/Movie (2020)/Movie (2020).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le",
			Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}

	h264SDR1080p := compliantHEVC10
	h264SDR1080p.Video = []transcode.VideoStream{{
		Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080,
		FrameRate: transcode.Rational{Num: 24, Den: 1},
	}}

	remuxAudio := compliantHEVC10
	remuxAudio.Audio = []transcode.AudioStream{{Codec: "eac3", Channels: 6, Language: "eng", Disposition: transcode.Disposition{Default: true}}}

	shortClip := h264SDR1080p
	shortClip.Format.Duration = 30 * time.Second

	remuxModifier := h264SDR1080p
	remuxModifier.Modifier = "remux"

	dv5NoVBV := h264SDR1080p
	dv5NoVBV.Video = append([]transcode.VideoStream(nil), h264SDR1080p.Video...)
	dv5NoVBV.Video[0].HDR = transcode.HDRInfo{Format: commonv1.HdrFormatDolbyVision, DolbyVision: &mediainfo.DoviRecord{Profile: 5}}

	dv5profile := defaultProfile() // no MaxRateKbps/BufSizeKbps set

	rejectPolicy := defaultProfile()
	rejectPolicy.HDR.DolbyVision = transcode.DolbyVisionReject

	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true}}
	meta := transcode.PlanMeta{ProfileName: "hevc10-aac-space", ProfileHash: "deadbeef", Threads: 8}

	cases := []struct {
		name    string
		info    transcode.MediaInfo
		profile transcode.ProfileSpec
		wantDec transcode.Decision
		wantWhy string // substring
	}{
		{"already compliant", compliantHEVC10, profile, transcode.DecisionSkip, "already compliant"},
		{"video not compliant -> encode", h264SDR1080p, profile, transcode.DecisionEncode, "hevc"},
		{"video compliant, audio not -> remux only", remuxAudio, profile, transcode.DecisionRemuxOnly, "remux"},
		{"below min duration -> skip", shortClip, profile, transcode.DecisionSkip, "minDuration"},
		{"never-transcode modifier -> skip", remuxModifier, profile, transcode.DecisionSkip, "neverTranscodeModifiers"},
		{"dv5 missing vbv -> reject", dv5NoVBV, dv5profile, transcode.DecisionReject, "maxRateKbps"},
		{"dv reject policy -> reject", dv5NoVBV, rejectPolicy, transcode.DecisionReject, "reject"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := transcode.Plan(tc.info, tc.profile, caps, meta)
			require.NoError(t, err)
			require.Equal(t, tc.wantDec, p.Decision)
			require.Contains(t, p.Reason, tc.wantWhy)
		})
	}
}

func TestFallbackTier(t *testing.T) {
	qsvOnly := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierQSV: true}}
	vaapiOnly := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierVAAPI: true}}
	neither := transcode.Capabilities{}

	got, ok := transcode.FallbackTier(transcode.TierQSV, qsvOnly)
	require.True(t, ok)
	require.Equal(t, transcode.TierQSV, got, "no fallback needed when the wanted tier is available")

	got, ok = transcode.FallbackTier(transcode.TierQSV, vaapiOnly)
	require.True(t, ok)
	require.Equal(t, transcode.TierVAAPI, got, "intel qsv falls back to vaapi on the same node")

	_, ok = transcode.FallbackTier(transcode.TierQSV, neither)
	require.False(t, ok, "no fallback exists when neither intel path is present")

	_, ok = transcode.FallbackTier(transcode.TierNVENC, neither)
	require.False(t, ok, "nvenc has no documented fallback")
}

// argAfter returns the argv element immediately following the first
// occurrence of flag, and whether flag was found at all.
func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestPlanAndArgsNeverPanicOnAZeroDenominatorFrameRate covers the same
// "malformed input never panics" constraint for a source whose FrameRate
// carries a zero denominator (division by zero) -- this belongs beside
// CRFFor/roundFPS, not the -progress parser: no ffmpeg -progress field ever
// carries a "num/den" frame rate, so the zero-denominator hazard lives in
// Rational/roundFPS (plan.go), not in progressFromFields (runner.go).
func TestPlanAndArgsNeverPanicOnAZeroDenominatorFrameRate(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080,
			FrameRate: transcode.Rational{Num: 24, Den: 0}, // malformed: division by zero
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng"}},
	}
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true}}
	meta := transcode.PlanMeta{ProfileName: "t", ProfileHash: "h", Threads: 8}

	p, err := transcode.Plan(info, defaultProfile(), caps, meta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, p.Decision)

	// roundFPS(Rational{24, 0}) is documented to return 0 rather than
	// dividing by zero; -g/-keyint_min render "0" instead of panicking.
	args := transcode.Args(p)
	g, ok := argAfter(args, "-g")
	require.True(t, ok)
	require.Equal(t, "0", g)

	keyintMin, ok := argAfter(args, "-keyint_min")
	require.True(t, ok)
	require.Equal(t, "0", keyintMin)
}

// TestPlanOnlyAppendsTheDV7DowngradeReasonWhenTheActiveModeActuallyDowngrades
// covers the controller ruling on the DV7 golden's reason string: the
// "downgraded to HDR10" suffix must be tied to hdr.dolbyVision actually
// being downgradeToHDR10, not merely to the source being profile 7 -- a
// profile-7 source under a passthrough policy never downgrades anything.
func TestPlanOnlyAppendsTheDV7DowngradeReasonWhenTheActiveModeActuallyDowngrades(t *testing.T) {
	dv7Info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080,
			FrameRate: transcode.Rational{Num: 24, Den: 1},
			HDR: transcode.HDRInfo{
				Format:      commonv1.HdrFormatDolbyVision,
				DolbyVision: &mediainfo.DoviRecord{Profile: 7, ELPresent: true, BLPresent: true},
			},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true}}
	meta := transcode.PlanMeta{ProfileName: "t", ProfileHash: "h", Threads: 8}

	downgrade := defaultProfile()
	downgrade.HDR.DolbyVision = transcode.DolbyVisionDowngradeToHDR10

	maxRate := int32(40000)
	bufSize := int32(60000)
	passthrough := defaultProfile() // HDR.DolbyVision defaults to Passthrough
	passthrough.Video.MaxRateKbps = &maxRate
	passthrough.Video.BufSizeKbps = &bufSize

	p, err := transcode.Plan(dv7Info, downgrade, caps, meta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, p.Decision)
	require.Contains(t, p.Reason, "profile 7 dual-layer Dolby Vision downgraded to HDR10",
		"downgradeToHDR10 must append the suffix")

	p, err = transcode.Plan(dv7Info, passthrough, caps, meta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, p.Decision)
	require.NotContains(t, p.Reason, "downgraded to HDR10",
		"a profile-7 source under a passthrough policy never downgrades anything")
}
