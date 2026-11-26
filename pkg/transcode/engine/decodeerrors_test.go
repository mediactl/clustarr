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
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// corruptFLAC is withAudio's 3 s clip with FLAC audio in 4096-sample
// frames, the audio packets damaged names (by their number from 0) having
// every fourth byte replaced, as the noise bitstream filter's amount=4 did.
func corruptFLAC(t *testing.T, damaged func(n int) bool) string {
	t.Helper()
	a := flacAudio
	a.Options = map[string]string{"frame_size": "4096"}
	clean := withAudio(t, "aformat=channel_layouts=stereo", a, 0)
	noise := rand.New(rand.NewPCG(1, 2))
	return testmedia.Remux(t, clean, "damaged.mkv", testmedia.Retag{Packet: func(stream, n int, data []byte) {
		if stream != 1 || !damaged(n) {
			return
		}
		for i := 0; i < len(data); i += 4 {
			data[i] = byte(noise.Uint32())
		}
	}})
}

// decodedSamples is how many audio samples FFmpeg decodes from path's
// first audio stream.
func decodedSamples(t *testing.T, path string) int {
	t.Helper()
	n := 0
	for _, f := range testmedia.Frames(t, path, 1) {
		n += f.Samples
	}
	return n
}

// A FLAC packet that will not decode -- 63 Up's ("invalid residual",
// 2026-10-15) -- is skipped, as FFmpeg's own decode skips it, and the
// encoder fills its samples with silence, so the audio keeps its length
// and stays in sync. Before, the first such packet failed the whole run.
func TestAnAudioPacketThatWillNotDecodeIsSkipped(t *testing.T) {
	src := corruptFLAC(t, func(n int) bool { return n == 10 })
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{
		SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000,
	}), src, out, Options{})
	require.NoError(t, err)
	// 3 s at 48 kHz; the dropped packet is 4096 samples.
	assert.InDelta(t, 3*48000, decodedSamples(t, out), 2048, "the skipped packet's samples are silence, not lost")
}

// A stream that will hardly decode at all still fails, as FFmpeg's does
// past its max_error_rate of 2/3.
func TestAStreamThatWillNotDecodeStillFails(t *testing.T) {
	src := corruptFLAC(t, func(int) bool { return true })
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{
		SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000,
	}), src, out, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would not decode")
}
