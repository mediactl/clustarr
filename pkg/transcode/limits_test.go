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
	"k8s.io/utils/ptr"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// The two messages hevc_nvenc gives about a setting its device cannot
// meet, exactly as the owner's RTX 2070 Max-Q printed them (2026-09-30).
const (
	nvencBFramesRefused = "[hevc_nvenc @ 0x55ac82933bc0] Max B-frames 8 exceed 5\n" +
		"[hevc_nvenc @ 0x55ac82933bc0] No capable devices found\n"
	nvencLookaheadClipped = "[hevc_nvenc @ 0x55f77a6f0c00] Clipping lookahead depth to 54 (from 64) due to lack of surfaces/delay"
)

func TestParseEncoderLimitMessages(t *testing.T) {
	bf, ok := transcode.ParseMaxBFrames(nvencBFramesRefused)
	require.True(t, ok)
	assert.Equal(t, int32(5), bf)
	la, ok := transcode.ParseLookaheadClip(nvencLookaheadClipped)
	require.True(t, ok)
	assert.Equal(t, int32(54), la)

	_, ok = transcode.ParseMaxBFrames("[hevc_nvenc @ 0x1] OpenEncodeSessionEx failed: out of memory")
	assert.False(t, ok)
	_, ok = transcode.ParseLookaheadClip("")
	assert.False(t, ok)
}

// Plan renders min(profile, device limit) for NVENC and says so in the
// reason, which is the job's Planned message; x265 has no device limits.
func TestPlanAppliesTheDeviceLimits(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/M (2020)/M (2020).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: transcode.Rational{Num: 24, Den: 1},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng", Disposition: transcode.Disposition{Default: true}}},
	}
	profile := defaultProfile()
	profile.Hardware = transcode.HardwareNVIDIA
	profile.Video.BFrames, profile.Video.RCLookahead = 8, 64
	profile.Video.NVENC = transcode.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"}
	limits := transcode.Limits{MaxBFrames: ptr.To[int32](5), MaxLookahead: ptr.To[int32](54)}
	caps := transcode.Capabilities{
		Encoders: map[transcode.Tier]bool{transcode.TierNVENC: true, transcode.TierCPUx265: true},
		Limits:   map[transcode.Tier]transcode.Limits{transcode.TierNVENC: limits, transcode.TierCPUx265: limits},
	}
	meta := transcode.PlanMeta{ProfileName: "p", ProfileHash: "h", Threads: 1}

	p, err := transcode.Plan(info, profile, caps, meta)
	require.NoError(t, err)
	require.Equal(t, transcode.TierNVENC, p.Tier)
	got, _ := argAfter(p.VideoArgs, "-bf")
	assert.Equal(t, "5", got)
	got, _ = argAfter(p.VideoArgs, "-rc-lookahead")
	assert.Equal(t, "54", got)
	assert.Contains(t, p.Reason, "bFrames 8 → 5 (device limit)")
	assert.Contains(t, p.Reason, "rcLookahead 64 → 54 (device limit)")

	profile.Video.BFrames = 4
	p, err = transcode.Plan(info, profile, caps, meta)
	require.NoError(t, err)
	got, _ = argAfter(p.VideoArgs, "-bf")
	assert.Equal(t, "4", got, "a profile value under the limit is kept")
	assert.NotContains(t, p.Reason, "bFrames")

	profile.Hardware = transcode.HardwareCPU
	profile.Video.BFrames = 8
	p, err = transcode.Plan(info, profile, caps, meta)
	require.NoError(t, err)
	require.Equal(t, transcode.TierCPUx265, p.Tier)
	got, _ = argAfter(p.VideoArgs, "-bf")
	assert.Equal(t, "8", got, "x265 has no device limits")
	assert.Contains(t, strings.Join(p.VideoArgs, " "), "rc-lookahead=64")
	assert.NotContains(t, p.Reason, "device limit")
}

// fakeFFmpeg is an ffmpeg that behaves as the RTX 2070 Max-Q did: -bf above
// 5 refuses to open the encoder, and -rc-lookahead above 54 is clipped.
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
args="$*"
bf=$(echo "$args" | sed -n 's/.*-bf \([0-9]*\).*/\1/p')
la=$(echo "$args" | sed -n 's/.*-rc-lookahead \([0-9]*\).*/\1/p')
if [ -n "$bf" ] && [ "$bf" -gt 5 ]; then
  printf '[hevc_nvenc @ 0x1] Max B-frames %s exceed 5\n[hevc_nvenc @ 0x1] No capable devices found\n' "$bf" >&2
  exit 1
fi
if [ -n "$la" ] && [ "$la" -gt 54 ]; then
  printf '[hevc_nvenc @ 0x1] Clipping lookahead depth to 54 (from %s) due to lack of surfaces/delay\n' "$la" >&2
fi
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func TestProbeLimitsMeasuresTheDevicesOwnLimits(t *testing.T) {
	ffmpeg := fakeFFmpeg(t)
	v := transcode.VideoSpec{BFrames: 8, RCLookahead: 64}
	l, err := transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierNVENC, v)
	require.NoError(t, err)
	require.NotNil(t, l.MaxBFrames)
	assert.Equal(t, int32(5), *l.MaxBFrames)
	require.NotNil(t, l.MaxLookahead)
	assert.Equal(t, int32(54), *l.MaxLookahead)

	l, err = transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierNVENC, transcode.VideoSpec{BFrames: 4, RCLookahead: 32})
	require.NoError(t, err)
	assert.Nil(t, l.MaxBFrames, "the profile's values open the encoder: no limit is known")
	assert.Nil(t, l.MaxLookahead)

	l, err = transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierCPUx265, v)
	require.NoError(t, err)
	assert.Equal(t, transcode.Limits{}, l, "libx265 has no device limits and runs no trial")

	broken := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(broken, []byte("#!/bin/sh\necho 'No NVENC capable devices found' >&2\nexit 1\n"), 0o755))
	_, err = transcode.ProbeLimits(context.Background(), broken, transcode.TierNVENC, v)
	assert.Error(t, err, "an encoder that will not open for another reason is an error, not a limit")
}

// On a host whose ffmpeg can open hevc_nvenc (a CUDA transcoder pod, or a
// workstation with an NVIDIA GPU), ProbeLimits measures the real device:
// asking for more B-frames than any NVENC generation encodes (16) must come
// back with a limit below it. Skipped anywhere NVENC does not open.
func TestProbeLimitsOnRealNVENC(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH")
	}
	if _, err := transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierNVENC, transcode.VideoSpec{BFrames: 0, RCLookahead: 0}); err != nil {
		t.Skipf("hevc_nvenc does not open on this host: %v", err)
	}
	l, err := transcode.ProbeLimits(context.Background(), ffmpeg, transcode.TierNVENC, transcode.VideoSpec{BFrames: 16, RCLookahead: 8})
	require.NoError(t, err)
	require.NotNil(t, l.MaxBFrames, "16 B-frames opened: no NVENC generation encodes that many")
	assert.Less(t, *l.MaxBFrames, int32(16))
	t.Logf("this device encodes at most %d B-frames", *l.MaxBFrames)
}
