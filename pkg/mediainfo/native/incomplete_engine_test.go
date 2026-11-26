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

package native_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// This file is the one test of the package that runs the transcode engine,
// which imports this package: it is an external test (native_test) so the
// two do not form an import cycle, and reaches the package's test seams
// through export_test.go.

// hdr10WithStreamSideData is an HDR10 file whose Matroska Colour element
// carries the mastering display and light level, as most HDR10 releases
// (and every HDR10 output of the engine) do: that side data is the
// stream-level evidence the probe has without the frame.
func hdr10WithStreamSideData(t *testing.T) string {
	t.Helper()
	src := testmedia.Make(t, "hdr10.mkv", testmedia.Spec{Video: []testmedia.Video{{
		Source: "testsrc2=duration=1:size=320x240:rate=25", Encoder: "libx265", PixFmt: "yuv420p10le",
		Options: map[string]string{"preset": "ultrafast", "x265-params": "log-level=error"},
		Color:   testmedia.Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc"},
		HDR10: &testmedia.HDR10{
			MasteringDisplay: [10]uint32{13250, 34500, 7500, 3000, 34000, 16000, 15635, 16450, 10000000, 1},
			MaxCLL:           1000, MaxFALL: 400,
		},
	}}})
	out := filepath.Join(t.TempDir(), "out.mkv")
	plan := standard.Result{
		Decision: standard.DecisionEncode, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{
			Action: "encode", Encoder: "libx265", Decode: "cpu", Filter: "format=yuv420p10le", HDR: "hdr10",
			Options: map[string]string{"preset": "ultrafast", "x265-params": "log-level=error"},
			Color:   standard.ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"},
		},
	}
	_, err := engine.Run(context.Background(), plan, src, out, engine.Options{})
	require.NoError(t, err)
	return out
}

// The in-process probe applies mediainfo's rule: a first frame it cannot
// read, on a stream that says the video may be HDR, is an incomplete probe
// -- an error -- and never an SDR reading.
func TestTheInProcessProbeRefusesAnHDR10FileWhoseFirstFrameCannotBeRead(t *testing.T) {
	p := native.ProberForTest(t)
	path := hdr10WithStreamSideData(t)
	mi, raw, err := p.Probe(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "read whole, the file is HDR10")
	require.NoError(t, raw.FrameErr)

	cause := errors.New("decoder: invalid data")
	native.FailFirstFrameForTest(t, cause)
	mi, raw, err = p.Probe(context.Background(), path)
	require.ErrorIs(t, err, mediainfo.ErrIncompleteProbe)
	assert.ErrorIs(t, err, cause)
	assert.Nil(t, mi)
	assert.Nil(t, raw)
}
