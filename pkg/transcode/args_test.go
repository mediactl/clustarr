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
// repo-root testdata/transcode/ tree is reached via ../../.
func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "transcode", "golden", name+".golden")
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
