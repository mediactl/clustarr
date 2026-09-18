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

package mediainfo

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestBitDepthFromPixFmt(t *testing.T) {
	cases := map[string]int32{
		"yuv420p": 8, "yuv420p10le": 10, "yuv420p12le": 12, "yuv444p10le": 10, "": 8,
	}
	for in, want := range cases {
		assert.Equal(t, want, bitDepthFromPixFmt(in), in)
	}
}

func TestFrameRateMilli(t *testing.T) {
	cases := map[string]int32{
		"24/1": 24000, "24000/1001": 23976, "30/1": 30000, "0/0": 0, "": 0,
	}
	for in, want := range cases {
		assert.Equal(t, want, frameRateMilli(in), in)
	}
}

func TestKbpsFromBitRate(t *testing.T) {
	assert.Equal(t, int32(477), kbpsFromBitRate("477224"))
	assert.Equal(t, int32(0), kbpsFromBitRate(""))
	assert.Equal(t, int32(0), kbpsFromBitRate("not-a-number"))
}

func TestContainerFromPath(t *testing.T) {
	assert.Equal(t, "mp4", containerFromPath("testdata/mediainfo/sample_h264_8bit.mp4"))
	assert.Equal(t, "mkv", containerFromPath("/data/movies/Foo/Foo.MKV"))
}

func TestClassifyHDR(t *testing.T) {
	tests := map[string]struct {
		raw  *Raw
		want commonv1.HdrFormat
	}{
		"nil raw":                      {nil, commonv1.HdrFormatNone},
		"no colour tags":               {&Raw{}, commonv1.HdrFormatNone},
		"hlg":                          {&Raw{ColorTransfer: "arib-std-b67"}, commonv1.HdrFormatHLG10},
		"pq10, smpte2084 no mdcv":      {&Raw{ColorTransfer: "smpte2084"}, commonv1.HdrFormatPQ10},
		"hdr10, smpte2084 with mdcv":   {&Raw{ColorTransfer: "smpte2084", MasteringDisplay: &MasteringDisplay{}}, commonv1.HdrFormatHDR10},
		"hdr10plus":                    {&Raw{ColorTransfer: "smpte2084", MasteringDisplay: &MasteringDisplay{}, HasHDR10Plus: true}, commonv1.HdrFormatHDR10Plus},
		"dv profile 5":                 {&Raw{Dovi: &DoviRecord{Profile: 5, BLSignalCompatibilityID: 0}}, commonv1.HdrFormatDolbyVision},
		"dv profile 7 (EL dropped)":    {&Raw{Dovi: &DoviRecord{Profile: 7, BLSignalCompatibilityID: 6}}, commonv1.HdrFormatDolbyVisionHDR10},
		"dv profile 8, compat 1 hdr10": {&Raw{Dovi: &DoviRecord{Profile: 8, BLSignalCompatibilityID: 1}}, commonv1.HdrFormatDolbyVisionHDR10},
		"dv profile 8, compat 2 sdr":   {&Raw{Dovi: &DoviRecord{Profile: 8, BLSignalCompatibilityID: 2}}, commonv1.HdrFormatDolbyVisionSDR},
		"dv profile 8, compat 4 hlg":   {&Raw{Dovi: &DoviRecord{Profile: 8, BLSignalCompatibilityID: 4}}, commonv1.HdrFormatDolbyVisionHLG},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyHDR(tc.raw))
		})
	}
}
