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

package inprocess

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func failFirstFrame(t *testing.T, err error) {
	t.Helper()
	old := readFirstFrame
	t.Cleanup(func() { readFirstFrame = old })
	readFirstFrame = func(*ffgo.Decoder, *ffgo.StreamInfo, *mediainfo.Raw) error { return err }
}

// hdr10WithStreamSideData is an HDR10 file whose Matroska Colour element
// carries the mastering display and light level, as most HDR10 releases
// (and every HDR10 output of the engine) do: ffgo reads no stream colour
// tags, so that side data is the evidence its probe has without the frame.
func hdr10WithStreamSideData(t *testing.T) string {
	t.Helper()
	src := ffmpegClip(t, "hdr10.mkv", "-f", "lavfi", "-i", "testsrc2=duration=1:size=320x240:rate=25",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params",
		"log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")
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
	ffmpeg9OrSkip(t)
	path := hdr10WithStreamSideData(t)
	mi, raw, err := Engine{}.Probe(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "read whole, the file is HDR10")
	require.NoError(t, raw.FrameErr)

	cause := errors.New("decoder: invalid data")
	failFirstFrame(t, cause)
	mi, raw, err = Engine{}.Probe(context.Background(), path)
	require.ErrorIs(t, err, mediainfo.ErrIncompleteProbe)
	assert.ErrorIs(t, err, cause)
	assert.Nil(t, mi)
	assert.Nil(t, raw)
}

// A stream that says nothing of HDR reads as SDR without its frame, as
// mediainfo's probe does, and the failure is recorded on Raw for the
// worker, which refuses every incomplete probe.
func TestTheInProcessProbeRecordsAFirstFrameItCannotRead(t *testing.T) {
	ffmpeg9OrSkip(t)
	path := ffmpegClip(t, "sdr.mkv", "-f", "lavfi", "-i", "testsrc2=duration=1:size=320x180:rate=24", "-c:v", "libx264", "-preset", "ultrafast")
	cause := errors.New("decoder: invalid data")
	failFirstFrame(t, cause)
	mi, raw, err := Engine{}.Probe(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	assert.ErrorIs(t, raw.FrameErr, cause)
}
