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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func bitDepthSource(codec, pixfmt string, height int32, hdr commonv1.HdrFormat) transcode.MediaInfo {
	return transcode.MediaInfo{
		Path:   "/media/movies/M (2020)/M (2020).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: codec, PixFmt: pixfmt, Width: height * 16 / 9, Height: height, FrameRate: fps24(),
			HDR: transcode.HDRInfo{Format: hdr},
		}},
		Audio: []transcode.AudioStream{{Codec: "aac", Channels: 2, Language: "eng"}},
	}
}

func planOn(t *testing.T, info transcode.MediaInfo, hw transcode.Hardware) *transcode.PlanResult {
	t.Helper()
	p := defaultProfile()
	p.Hardware = hw
	res, err := transcode.Plan(info, p, testCaps, testMeta)
	require.NoError(t, err)
	return res
}

// Spec §5 as the owner set it (2026-10-01): an SDR source at 1080p or less
// is encoded HEVC Main, 8-bit; HDR (whose transfer needs 10 bits) and
// anything larger stay Main 10.
func TestSDRAt1080pOrLessEncodesEightBit(t *testing.T) {
	for _, h := range []int32{1080, 720, 480} {
		src := bitDepthSource("h264", "yuv420p", h, commonv1.HdrFormatNone)
		for hw, want := range map[transcode.Hardware][]string{
			transcode.HardwareCPU:    {"-pix_fmt yuv420p ", "-profile:v main "},
			transcode.HardwareNVIDIA: {"scale_cuda=format=nv12", "-profile:v main "},
			transcode.HardwareIntel:  {"vpp_qsv=format=nv12"},
		} {
			res := planOn(t, src, hw)
			require.Equal(t, transcode.DecisionEncode, res.Decision)
			args := strings.Join(transcode.Args(res), " ") + " "
			for _, w := range want {
				assert.Contains(t, args, w, "%dp on %s", h, hw)
			}
			assert.NotContains(t, args, "10le", "%dp on %s", h, hw)
			assert.NotContains(t, args, "p010", "%dp on %s", h, hw)
			assert.Equal(t, "yuv420p", res.Expect.PixelFormat, "%dp on %s", h, hw)
		}
	}
}

func TestNVENCWithoutNVDECConvertsToEightBitOnTheCPU(t *testing.T) {
	// 4:2:2 is not on NVDEC's list, so the frames come up from the CPU.
	res := planOn(t, bitDepthSource("h264", "yuv422p", 1080, commonv1.HdrFormatNone), transcode.HardwareNVIDIA)
	args := strings.Join(transcode.Args(res), " ") + " "
	assert.Contains(t, args, "-pix_fmt nv12 ")
	assert.Contains(t, args, "-profile:v main ")
}

func TestHDRAndUHDStayMain10(t *testing.T) {
	for name, src := range map[string]transcode.MediaInfo{
		"hdr10 1080p": bitDepthSource("h264", "yuv420p10le", 1080, commonv1.HdrFormatHDR10),
		"sdr 2160p":   bitDepthSource("h264", "yuv420p", 2160, commonv1.HdrFormatNone),
	} {
		for _, hw := range []transcode.Hardware{transcode.HardwareCPU, transcode.HardwareNVIDIA} {
			res := planOn(t, src, hw)
			args := strings.Join(transcode.Args(res), " ") + " "
			assert.Contains(t, args, "-profile:v main10 ", "%s on %s", name, hw)
			assert.Equal(t, "yuv420p10le", res.Expect.PixelFormat, "%s on %s", name, hw)
		}
	}
}

func TestEightBitHEVCAt1080pIsCompliant(t *testing.T) {
	src := bitDepthSource("hevc", "yuv420p", 1080, commonv1.HdrFormatNone)
	src.Video[0].Profile = "Main"
	assert.Equal(t, transcode.DecisionSkip, planOn(t, src, transcode.HardwareCPU).Decision)

	uhd := bitDepthSource("hevc", "yuv420p", 2160, commonv1.HdrFormatNone)
	uhd.Video[0].Profile = "Main"
	assert.Equal(t, transcode.DecisionEncode, planOn(t, uhd, transcode.HardwareCPU).Decision, "above 1080p the standard is still Main 10")

	ten := bitDepthSource("hevc", "yuv420p10le", 1080, commonv1.HdrFormatNone)
	ten.Video[0].Profile = "Main 10"
	assert.Equal(t, transcode.DecisionSkip, planOn(t, ten, transcode.HardwareCPU).Decision, "a 10-bit 1080p file is never encoded down")
}
