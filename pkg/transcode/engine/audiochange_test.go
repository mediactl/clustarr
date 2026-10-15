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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// layoutChangeClip is a 3 s clip whose one AC-3 track is stereo for its
// first half and 5.1 for its second: two segments joined by FFmpeg's concat
// demuxer, the packets copied, as a broadcast recording or a DTS track that
// moves between core and extension carries a change mid-stream.
func layoutChangeClip(t *testing.T) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	var list string
	for _, l := range []string{"stereo", "5.1"} {
		seg := filepath.Join(dir, "seg-"+l+".mkv")
		run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=1.5",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1.5,aformat=channel_layouts="+l,
			"-c:v", "libx264", "-preset", "ultrafast", "-bf", "0", "-c:a", "ac3", seg)
		list += "file '" + seg + "'\n"
	}
	listFile := filepath.Join(dir, "list.txt")
	require.NoError(t, os.WriteFile(listFile, []byte(list), 0o644))
	out := filepath.Join(dir, "joined.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", listFile, "-c", "copy", out)
	return out
}

// An audio track whose layout changes mid-stream encodes through: the
// stage builds its resampler from the first decoded frame, and a frame in
// another layout, format or rate gets a new one, the old one's tail flushed
// first, as FFmpeg's audio filter graph reconfigures on a changed input.
// Before, every later frame failed (Back to the Future's DTS track,
// "swr_convert_frame failed: -1668179713", AVERROR_INPUT_CHANGED,
// 2026-10-15), five attempts in a row.
func TestAnAudioTrackWhoseLayoutChangesMidStreamEncodes(t *testing.T) {
	src := layoutChangeClip(t)
	for name, outs := range map[string][]standard.AudioPlan{
		"AAC 2.0":              {{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000}},
		"AAC 2.0 and AC-3 5.1": {{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 160000}, {SourceIndex: 0, Action: "ac3", Channels: 6, Layout: "5.1", BitRate: 640000}},
	} {
		t.Run(name, func(t *testing.T) {
			plan := audioPlan(outs[0])
			plan.Audio = outs
			out := filepath.Join(t.TempDir(), "o.mkv")
			_, err := Run(context.Background(), plan, src, out, Options{})
			require.NoError(t, err)
			p := ffprobeJSON(t, out)
			require.Len(t, p.Streams, 1+len(outs))
			assert.InDelta(t, 3.0, p.seconds(t), 0.2)
			// Both halves are encoded: 3 s at 48 kHz, give or take a frame.
			assert.InDelta(t, 3*48000, decodedSamples(t, out), 2048)
		})
	}
}
