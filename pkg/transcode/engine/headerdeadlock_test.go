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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// manyAudioClip is a 2 s clip with n AC-3 tracks, as a release carrying a
// dub per language does.
func manyAudioClip(t *testing.T, n int) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), "many.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-map", "0"}
	for range n {
		args = append(args, "-map", "1")
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "ac3", out)
	run(t, "ffmpeg", args...)
	return out
}

// The header waits for every encoder, and an encoder holds its lock while
// it hands the muxer a packet (ffgo's AudioEncoder.Encode): if the muxer
// stopped reading to write the header, a stage blocked in that hand-off kept
// the lock the header's Parameters() call needed, and the run hung forever
// -- Beetlejuice Beetlejuice's 17 audio stages, 40 minutes at 1m CPU
// (2026-10-15). The muxer keeps taking packets while it writes the header.
func TestManyEncodedStreamsNeverDeadlockTheHeader(t *testing.T) {
	const n = 12
	src := manyAudioClip(t, n)
	plan := standard.Result{Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMP4,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"}}
	for i := range n {
		plan.Audio = append(plan.Audio, standard.AudioPlan{SourceIndex: int32(i), Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000})
	}
	for attempt := range 5 {
		out := filepath.Join(t.TempDir(), "out.part.mp4")
		done := make(chan error, 1)
		go func() {
			_, err := Run(context.Background(), plan, src, out, Options{QueueDepth: 1})
			done <- err
		}()
		select {
		case err := <-done:
			require.NoError(t, err, "attempt %d", attempt)
		case <-time.After(60 * time.Second):
			t.Fatalf("attempt %d: the run hung writing the header (a stage holding its encoder's lock)", attempt)
		}
	}
}
