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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// An HDR source's plan expects an HDR output: the transfer and primaries
// the plan tags it with, and HDR10's static metadata whenever the source
// has it. Verify checks the output against exactly this before the swap.
func TestAnHDRPlanExpectsItsColourAndStaticMetadata(t *testing.T) {
	hdr10 := video("hevc", "yuv420p10le", 10, commonv1.HdrFormatHDR10)
	hdr10.Codec = "h264" // not compliant: encoded
	hdr10.HDR.MasteringDisplay = &mediainfo.MasteringDisplay{MaxLuminance: 10000000, MinLuminance: 1}
	hdr10.HDR.ContentLight = &mediainfo.ContentLight{MaxCLL: 1000, MaxFALL: 400}
	pq10 := video("h264", "yuv420p10le", 10, commonv1.HdrFormatPQ10)
	hlg := video("h264", "yuv420p10le", 10, commonv1.HdrFormatHLG10)
	copied := video("hevc", "yuv420p10le", 10, commonv1.HdrFormatHDR10)
	copied.HDR.MasteringDisplay = hdr10.HDR.MasteringDisplay

	for name, c := range map[string]struct {
		v    transcode.VideoStream
		want Expectation
	}{
		"HDR10 with its metadata": {hdr10, Expectation{Transfer: "smpte2084", Primaries: "bt2020", MasteringDisplay: true, ContentLight: true}},
		"PQ10, none to keep":      {pq10, Expectation{Transfer: "smpte2084", Primaries: "bt2020"}},
		"HLG":                     {hlg, Expectation{Transfer: "arib-std-b67", Primaries: "bt2020"}},
		"HEVC HDR10 copied":       {copied, Expectation{Transfer: "smpte2084", Primaries: "bt2020", MasteringDisplay: true}},
		"SDR":                     {h264, Expectation{}},
	} {
		t.Run(name, func(t *testing.T) {
			p := Plan(info(c.v, audio(0, "dts", 2, "stereo", "eng")), profile, cpu) // dts: never skipped
			require.NotEqual(t, DecisionSkip, p.Decision, p.Reason)
			got := Expectation{
				Transfer: p.Expect.Transfer, Primaries: p.Expect.Primaries,
				MasteringDisplay: p.Expect.MasteringDisplay, ContentLight: p.Expect.ContentLight,
			}
			assert.Equal(t, c.want, got)
		})
	}
}

// The colour expectation follows from the plan's Video.HDR and the source's
// own metadata, so it is not part of the plan's identity: the controller
// plans from the stored summary, which has no side data, and the worker
// from a live probe, and the two must hash alike.
func TestTheColourExpectationIsNotPartOfTheHash(t *testing.T) {
	v := video("h264", "yuv420p10le", 10, commonv1.HdrFormatHDR10)
	summary := Plan(info(v), profile, cpu)
	v.HDR.MasteringDisplay = &mediainfo.MasteringDisplay{MaxLuminance: 10000000, MinLuminance: 1}
	live := Plan(info(v), profile, cpu)
	require.NotEqual(t, summary.Expect.MasteringDisplay, live.Expect.MasteringDisplay)
	assert.Equal(t, summary.Hash(), live.Hash())
}
