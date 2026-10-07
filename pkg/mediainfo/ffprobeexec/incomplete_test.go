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

package ffprobeexec

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func hdr10Clip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	skipIfNoFFprobe(t)
	path := filepath.Join(t.TempDir(), "hdr10.mkv")
	out, err := exec.Command("ffmpeg", append(append([]string{}, hdr10FixtureArgs...), path)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	return path
}

func failFrameProbe(t *testing.T, fd mediainfo.FrameProbeData, err error) {
	t.Helper()
	old := frameProbe
	t.Cleanup(func() { frameProbe = old })
	frameProbe = func(context.Context, string) (mediainfo.FrameProbeData, error) { return fd, err }
}

// An HDR10 file whose first frame cannot be read is a probe error the
// caller retries -- catalogarr's ProbeFailed -- never a probe that reads it
// as SDR: an SDR reading plans an 8-bit SDR encode at 1080p, and a
// transcoded file is final.
func TestProbeOfAnHDR10FileWhoseFirstFrameCannotBeReadIsAnError(t *testing.T) {
	path := hdr10Clip(t)
	for name, fail := range map[string]func(t *testing.T){
		"the frame probe fails":    func(t *testing.T) { failFrameProbe(t, mediainfo.FrameProbeData{}, errors.New("exit status 1")) },
		"the frame probe is empty": func(t *testing.T) { failFrameProbe(t, mediainfo.FrameProbeData{}, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			fail(t)
			mi, raw, err := Probe(context.Background(), path)
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
	skipIfNoFFprobe(t)
	failFrameProbe(t, mediainfo.FrameProbeData{}, errors.New("exit status 1"))
	mi, raw, err := Probe(context.Background(), "../../../test/data/mediainfo/sample_h264_8bit.mp4")
	require.NoError(t, err)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	assert.Error(t, raw.FrameErr)
}

// The real frame probe reads the HDR10 clip, so nothing is incomplete.
func TestProbeOfAReadableHDR10FileIsComplete(t *testing.T) {
	path := hdr10Clip(t)
	mi, raw, err := Probe(context.Background(), path)
	require.NoError(t, err)
	assert.NoError(t, raw.FrameErr)
	assert.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr)
}
