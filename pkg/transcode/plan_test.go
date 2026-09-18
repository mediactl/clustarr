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
