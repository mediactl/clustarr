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

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The AC-3 encoder takes 5.1 FLTP at 640 kbps: what the standard's ac3
// action asks for (MP4 standard spec §3).
func TestTheAC3EncoderOpensAtFivePointOne(t *testing.T) {
	ffmpeg9OrSkip(t)
	enc, err := ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
		EncoderName: "ac3", SampleRate: 48000, Layout: "5.1", BitRate: 640000,
		GlobalHeader: true, InputTimeBase: ffgo.NewRational(1, 48000),
	})
	require.NoError(t, err)
	require.NoError(t, enc.Close())
}

// E-AC-3 is copied into MP4 (ec-3): the owner's correction to the spec.
func TestEAC3IsCopiedIntoMP4(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "eac3.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2,aformat=channel_layouts=5.1",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error",
		"-c:a", "eac3", src)
	out := filepath.Join(dir, "out.mp4")
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMP4,
		Video: standard.VideoPlan{Action: "copy"},
		Audio: []standard.AudioPlan{{SourceIndex: 0, Action: "copy"}},
	}
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 2)
	assert.Equal(t, "eac3", p.Streams[1].CodecName)
	assert.Equal(t, 6, p.Streams[1].Channels)
	assert.Equal(t, "hevc", p.Streams[0].CodecName)
}
