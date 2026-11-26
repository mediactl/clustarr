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

package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// hdr10Clip is images/Dockerfile.e2e-fixtures's HDR10 clip.
func hdr10Clip(t *testing.T) string {
	t.Helper()
	return ffmpegClip(t, "hdr10.mkv", hdr10FixtureArgs...)
}

// failFirstFrame makes v:0's first frame unreadable: the native probe's
// equivalent of the ffprobe frame probe failing (spec 2026-10-06 §6.8).
func failFirstFrame(t *testing.T, err error) {
	t.Helper()
	old := readFirstFrame
	t.Cleanup(func() { readFirstFrame = old })
	readFirstFrame = func(*ffgo.Decoder, *ffgo.StreamInfo, *mediainfo.Raw) error { return err }
}

// An HDR10 file whose first frame cannot be read is a probe error the
// caller retries -- catalog's ProbeFailed -- never a probe that reads it
// as SDR: an SDR reading plans an 8-bit SDR encode at 1080p, and a
// transcoded file is final.
func TestProbeOfAnHDR10FileWhoseFirstFrameCannotBeReadIsAnError(t *testing.T) {
	p := testProber(t)
	path := hdr10Clip(t)
	for name, cause := range map[string]error{
		"the first frame does not decode":               errors.New("decoder: invalid data"),
		"the input ends before the first frame decodes": fmt.Errorf("the input ended before the first video frame decoded: %w", io.EOF),
	} {
		t.Run(name, func(t *testing.T) {
			failFirstFrame(t, cause)
			mi, raw, err := p.Probe(context.Background(), path)
			require.ErrorIs(t, err, mediainfo.ErrIncompleteProbe)
			assert.Nil(t, mi)
			assert.Nil(t, raw)
		})
	}
}

// An SDR file's stream says nothing of HDR, so a frame that cannot be
// read changes nothing it reports: the probe still succeeds, as SDR, and
// records why the frame is missing.
func TestProbeOfAnSDRFileWhoseFirstFrameCannotBeReadSucceeds(t *testing.T) {
	p := testProber(t)
	failFirstFrame(t, errors.New("decoder: invalid data"))
	mi, raw, err := p.Probe(context.Background(), "../../../test/data/mediainfo/sample_h264_8bit.mp4")
	require.NoError(t, err)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	assert.Error(t, raw.FrameErr)
}

// The real first frame of the HDR10 clip reads, so nothing is incomplete.
func TestProbeOfAReadableHDR10FileIsComplete(t *testing.T) {
	p := testProber(t)
	path := hdr10Clip(t)
	mi, raw, err := p.Probe(context.Background(), path)
	require.NoError(t, err)
	assert.NoError(t, raw.FrameErr)
	assert.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr)
}

// A stream that says nothing of HDR reads as SDR without its frame, and the
// failure is recorded on Raw for the worker, which refuses every incomplete
// probe.
func TestTheInProcessProbeRecordsAFirstFrameItCannotRead(t *testing.T) {
	p := testProber(t)
	path := ffmpegClip(t, "sdr.mkv", "-f", "lavfi", "-i", "testsrc2=duration=1:size=320x180:rate=24", "-c:v", "libx264", "-preset", "ultrafast")
	cause := errors.New("decoder: invalid data")
	failFirstFrame(t, cause)
	mi, raw, err := p.Probe(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	assert.ErrorIs(t, raw.FrameErr, cause)
}
