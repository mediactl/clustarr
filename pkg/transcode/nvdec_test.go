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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestNVDECKey(t *testing.T) {
	for _, tt := range []struct {
		codec, pixFmt, want string
	}{
		{"h264", "yuv420p", "h264:8"},
		{"h264", "yuvj420p", "h264:8"},
		{"h264", "yuv420p10le", "h264:10"},
		{"hevc", "yuv420p10le", "hevc:10"},
		{"hevc", "yuv420p12le", "hevc:12"},
		{"av1", "yuv420p", "av1:8"},
		{"h264", "yuv422p", ""},
		{"hevc", "yuv444p10le", ""},
		{"h264", "", ""},
	} {
		assert.Equal(t, tt.want, transcode.NVDECKey(transcode.VideoStream{Codec: tt.codec, PixFmt: tt.pixFmt}), "%s %s", tt.codec, tt.pixFmt)
	}
}

func TestNVDECDecodes(t *testing.T) {
	measured := func(formats map[string]bool) transcode.Limits {
		return transcode.Limits{NVDEC: &transcode.Decoders{Formats: formats}}
	}
	h264 := transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p"}
	hi10p := transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p10le"}
	av1 := transcode.VideoStream{Codec: "av1", PixFmt: "yuv420p"}
	vc1 := transcode.VideoStream{Codec: "vc1", PixFmt: "yuv420p"}
	h264422 := transcode.VideoStream{Codec: "h264", PixFmt: "yuv422p"}

	for _, tt := range []struct {
		name   string
		limits transcode.Limits
		v      transcode.VideoStream
		want   bool
	}{
		{"unmeasured: the static list decodes 8-bit H.264", transcode.Limits{}, h264, true},
		{"unmeasured: the static list never decodes Hi10P", transcode.Limits{}, hi10p, false},
		{"unmeasured: AV1 needs a measurement", transcode.Limits{}, av1, false},
		{"a measured failure overrides the static list", measured(map[string]bool{"h264:8": false}), h264, false},
		{"a measured success decodes what the static list does not", measured(map[string]bool{"av1:8": true}), av1, true},
		{"a format the trial did not test falls back to the static list", measured(map[string]bool{"h264:8": true}), vc1, true},
		{"4:2:2 is never decoded on NVDEC, whatever was measured", measured(map[string]bool{"h264:8": true}), h264422, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, transcode.NVDECDecodes(tt.limits, tt.v))
		})
	}
}

// A node's measured answer reaches the plan through Capabilities.Limits: a
// device whose trial failed on 8-bit H.264 is planned the software way.
func TestPlanDecodesOnTheCPUWhereTheDeviceMeasuredNoNVDEC(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{{Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1}}},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2}},
	}
	profile := nvencProfile()
	caps := transcode.Capabilities{
		Encoders: map[transcode.Tier]bool{transcode.TierNVENC: true},
		Limits: map[transcode.Tier]transcode.Limits{
			transcode.TierNVENC: {NVDEC: &transcode.Decoders{Formats: map[string]bool{"h264:8": false}}},
		},
	}
	plan, err := transcode.Plan(info, profile, caps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.TierNVENC, plan.Tier)
	assert.Empty(t, plan.HWInit)
	assert.Empty(t, plan.Filters)
	assert.Contains(t, plan.VideoArgs, "-pix_fmt")
	assert.Contains(t, plan.Reason, "decoding on the CPU (NVDEC does not decode h264 8-bit)")
}

// The decoder's surface pool is sized from the values the encode actually
// runs at: a device limit that clamps the B-frames clamps the pool too.
func TestNVDECExtraFramesFollowTheClampedEncode(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{{Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1}}},
		Audio:  []transcode.AudioStream{{Codec: "aac", Channels: 2}},
	}
	five := int32(5)
	caps := transcode.Capabilities{
		Encoders: map[transcode.Tier]bool{transcode.TierNVENC: true},
		Limits:   map[transcode.Tier]transcode.Limits{transcode.TierNVENC: {MaxBFrames: &five}},
	}
	plan, err := transcode.Plan(info, nvencProfile(), caps, testMeta)
	require.NoError(t, err)
	assert.Equal(t, []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda", "-extra_hw_frames", "49"}, plan.HWInit,
		"rc-lookahead 40 + 5 B-frames (the device's) + 4")
}

// ProbeDecoders against an ffmpeg that has no AV1 encoder and whose NVDEC,
// like Turing's, will not decode H.264 10-bit. Its decode trial is accepted
// only with the input options and filter the plan renders, so a trial that
// drifted from the plan would measure nothing true.
func TestProbeDecodersMeasuresWhatTheDeviceDecodes(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpeg, []byte(`#!/bin/sh
args="$*"
case "$args" in
*lavfi*)
  case "$args" in *libsvtav1*) echo "Unknown encoder 'libsvtav1'" >&2; exit 1;; esac
  exit 0;;
esac
case "$args" in
*"-hwaccel cuda -hwaccel_output_format cuda -extra_hw_frames "*"-vf scale_cuda=format=p010le -c:v hevc_nvenc"*) ;;
*) echo "not the plan's decode: $args" >&2; exit 1;;
esac
case "$args" in *h264-10.mkv*) echo "Impossible to convert between the formats" >&2; exit 1;; esac
exit 0
`), 0o755))

	d, err := transcode.ProbeDecoders(context.Background(), ffmpeg)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{
		"h264:8": true, "h264:10": false, "hevc:8": true, "hevc:10": true,
		"vp9:8": true, "vp9:10": true, "mpeg2video:8": true,
	}, d.Formats, "AV1 has no sample here, so it is unmeasured rather than false")
	assert.Equal(t, []string{"h264:8", "hevc:10", "hevc:8", "mpeg2video:8", "vp9:10", "vp9:8"}, d.Decodable())
}

// On a host with an NVIDIA GPU and an ffmpeg that has NVDEC, the real trial
// must decode 8-bit H.264, which every NVDEC does. Skipped elsewhere.
func TestProbeDecodersOnRealNVDEC(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH")
	}
	if _, err := transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierNVENC, transcode.VideoSpec{}); err != nil {
		t.Skipf("hevc_nvenc does not open on this host: %v", err)
	}
	d, err := transcode.ProbeDecoders(context.Background(), ffmpeg)
	require.NoError(t, err)
	t.Logf("NVDEC decodes %v", d.Decodable())
	if ok, measured := d.Formats["h264:8"]; measured {
		assert.True(t, ok, "every NVDEC decodes 8-bit H.264")
	}
}

// eac3Source is the owner's case (2026-09-30): 8-bit H.264 with two E-AC-3
// 5.1 tracks, which a GPU pool re-encoded to AAC at 384k on the CPU.
func eac3Source(video transcode.VideoStream) transcode.MediaInfo {
	return transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{video},
		Audio: []transcode.AudioStream{
			{Index: 0, Codec: "eac3", Channels: 6, Language: "eng", Disposition: transcode.Disposition{Default: true}, Atmos: true},
			{Index: 1, Codec: "eac3", Channels: 6, Language: "spa"},
		},
	}
}

// audio.copyCodecs copies a listed codec's tracks in place of the re-encode:
// one track each, even where keepOriginal (atmos) would have added the
// original beside a re-encode.
func TestCopyCodecsCopiesListedAudioInsteadOfReencoding(t *testing.T) {
	profile := nvencProfile()
	profile.Audio.KeepOriginal = transcode.KeepOriginalAtmos
	profile.Audio.CopyCodecs = []string{"eac3"}
	plan, err := transcode.Plan(eac3Source(transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1}}),
		profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Len(t, plan.Audio, 2)
	for _, a := range plan.Audio {
		assert.Equal(t, transcode.AudioActionCopy, a.Action)
		assert.Equal(t, "copy", a.Codec)
	}
	assert.True(t, plan.Audio[0].Default, "a copied track keeps its default disposition")
	args := strings.Join(transcode.Args(plan), " ")
	assert.Contains(t, args, "-c:a:0 copy -metadata:s:a:0 language=eng -disposition:a:0 default -c:a:1 copy -metadata:s:a:1 language=spa")
	assert.NotContains(t, args, "-b:a:")

	profile.Audio.CopyCodecs = nil
	plan, err = transcode.Plan(eac3Source(transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1}}),
		profile, testCaps, testMeta)
	require.NoError(t, err)
	require.Len(t, plan.Audio, 3, "unset: both re-encoded, and the Atmos original kept beside its re-encode, as before")
	assert.Equal(t, transcode.AudioActionEncode, plan.Audio[0].Action)
}

// A file whose video is already compliant and whose audio is only in copied
// codecs has nothing left to do: it is skipped, not remuxed for nothing.
func TestCopyCodecsCountAsCompliant(t *testing.T) {
	compliant := transcode.VideoStream{Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1}}
	profile := nvencProfile()
	profile.Audio.CopyCodecs = []string{"eac3"}
	plan, err := transcode.Plan(eac3Source(compliant), profile, testCaps, testMeta)
	require.NoError(t, err)
	assert.Equal(t, transcode.DecisionSkip, plan.Decision)

	profile.Audio.CopyCodecs = nil
	plan, err = transcode.Plan(eac3Source(compliant), profile, testCaps, testMeta)
	require.NoError(t, err)
	assert.NotEqual(t, transcode.DecisionSkip, plan.Decision, "unset: the E-AC-3 still needs re-encoding")
}
