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

// pcmClip is a 2 s Matroska clip whose audio is PCM in layout l. Matroska
// stores a PCM track's channel count and no layout, so its frames decode
// with the layout unspecified ("2 channels").
func pcmClip(t *testing.T, l string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), "pcm.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2,aformat=channel_layouts="+l,
		"-c:v", "libx264", "-preset", "ultrafast", "-bf", "0", "-c:a", "pcm_s24le", out)
	return out
}

// A Blu-ray's PCM track in Matroska encodes. Its frames' unspecified layout
// failed every attempt on the first frame (swr_convert_frame's
// AVERROR_INPUT_CHANGED, owner 2026-11-02: Fate/Grand Order Babylonia
// S01E10's pcm_s24le, five attempts on the intel pool) until ffgo's
// resampler gave such a frame its context's input layout.
func TestAPCMTrackInMatroskaEncodes(t *testing.T) {
	aac := standard.AudioPlan{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000}
	for name, tc := range map[string]struct {
		layout string
		outs   []standard.AudioPlan
	}{
		"stereo as AAC 2.0":           {"stereo", []standard.AudioPlan{aac}},
		"5.1 as AAC 2.0 and AC-3 5.1": {"5.1", []standard.AudioPlan{aac, {SourceIndex: 0, Action: "ac3", Channels: 6, Layout: "5.1", BitRate: 640000}}},
	} {
		t.Run(name, func(t *testing.T) {
			src := pcmClip(t, tc.layout)
			plan := audioPlan(tc.outs[0])
			plan.Audio = tc.outs
			out := filepath.Join(t.TempDir(), "o.mkv")
			_, err := Run(context.Background(), plan, src, out, Options{})
			require.NoError(t, err)
			require.Len(t, ffprobeJSON(t, out).Streams, 1+len(tc.outs))
			assert.InDelta(t, 2*48000, decodedSamples(t, out), 2048)
		})
	}
}
