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

package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

var hdr10Color = standard.ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}

// hdr10Source is an HDR10 clip: PQ, BT.2020, and SMPTE ST 2086 mastering
// display and MaxCLL/MaxFALL in its first frame.
func hdr10Source(t *testing.T) string {
	return videoClip(t, "hdr10.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx265",
		"-x265-params", "log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")
}

func hdr10Expectation() standard.Expectation {
	return standard.Expectation{
		VideoStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p10le", DurationMillis: 2000,
		Transfer: "smpte2084", Primaries: "bt2020", MasteringDisplay: true, ContentLight: true,
	}
}

func encodeTo(t *testing.T, src string, v standard.VideoPlan) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), encodePlan(v), src, out, Options{})
	require.NoError(t, err)
	return out
}

// The engine's HDR10 encode -- the plan's colour as encoder options, the
// source's static metadata passed to the encoder and the muxer -- passes
// the colour check.
func TestVerifyPassesACorrectHDR10Output(t *testing.T) {
	src := hdr10Source(t)
	v := cpuVideo("hdr10", hdr10Color)
	v.StripSideData = []string{"hdr10plus"}
	out := encodeTo(t, src, v)

	r, err := Verify(context.Background(), src, out, hdr10Expectation())
	require.NoError(t, err)
	assert.True(t, r.OK, "%v", r.Problems)
}

// An output that lost what made its source HDR fails verification -- exit
// 4, before the swap -- naming what is missing, however its stream counts,
// codec and duration check out.
func TestVerifyFailsAnOutputThatLostItsHDR(t *testing.T) {
	src := hdr10Source(t)

	// Encoded as SDR: the source's colour description is copied from its
	// first frame, so the transfer survives, but no static metadata does.
	sdrOfHDR := encodeTo(t, src, cpuVideo("sdr", standard.ColorTags{}))
	r, err := Verify(context.Background(), src, sdrOfHDR, hdr10Expectation())
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Contains(t, joined(r.Problems), "mastering display")
	assert.Contains(t, joined(r.Problems), "content light level")

	// Tagged BT.709 throughout: transfer and primaries are wrong too.
	bt709 := encodeTo(t, src, cpuVideo("sdr", standard.ColorTags{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: "tv"}))
	r, err = Verify(context.Background(), src, bt709, hdr10Expectation())
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Contains(t, joined(r.Problems), "transfer bt709, want smpte2084")
	assert.Contains(t, joined(r.Problems), "primaries bt709, want bt2020")
}

// An SDR expectation checks no colour: the existing SDR outputs verify as
// before.
func TestVerifyChecksNoColourForAnSDRPlan(t *testing.T) {
	src, out := hevcOutput(t)
	exp := standard.Expectation{VideoStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p10le", DurationMillis: 2000}
	r, err := Verify(context.Background(), src, out, exp)
	require.NoError(t, err)
	assert.True(t, r.OK, "%v", r.Problems)
}
