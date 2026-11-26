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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// subtitleTailClip is a 3 s clip, video and audio, whose subtitle's last
// cue ends at 8 s, in container ext: in Matroska the container's duration
// is the subtitle's.
func subtitleTailClip(t *testing.T, ext string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	sub := testmedia.Subtitle{Codec: "subrip", Events: []testmedia.Event{
		{Start: time.Second, End: 2 * time.Second, Text: "Hello"},
		{Start: 6 * time.Second, End: 8 * time.Second, Text: "The end"},
	}}
	if ext == "mp4" {
		// mov_text: a sample is its text behind a two-byte length.
		sub.Codec = "mov_text"
		for i, ev := range sub.Events {
			sub.Events[i].Text = string([]byte{byte(len(ev.Text) >> 8), byte(len(ev.Text))}) + ev.Text
		}
	}
	return testmedia.Make(t, "src."+ext, testmedia.Spec{
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24:duration=3", Encoder: "libx265",
			Options: map[string]string{"preset": "ultrafast", "x265-params": "log-level=error"},
		}},
		Audio:     []testmedia.Audio{{Source: "sine=frequency=440:sample_rate=48000:duration=3"}},
		Subtitles: []testmedia.Subtitle{sub},
	})
}

// A source whose subtitle outlasts its picture -- Ad Astra's last cue ends
// 2.6 s and Baddies East S01E11's 33 s after its video (2026-10-15) -- has
// a container duration its output cannot match: the output carries the
// subtitle as a sidecar and ends with its video and audio. The expected
// duration, the source's container duration, is then when the source's
// video and audio end, by their DURATION tags (Matroska records no stream
// duration; MP4's duration is its tracks' own, so the subtitle never
// stretches it).
func TestAnOutputEndsWhereTheSourcesVideoAndAudioEnd(t *testing.T) {
	for _, ext := range []string{"mkv"} {
		t.Run(ext, func(t *testing.T) {
			src := subtitleTailClip(t, ext)
			require.InDelta(t, 8.0, ffprobeJSON(t, src).seconds(t), 0.2, "the subtitle sets the container's duration")
			out := filepath.Join(t.TempDir(), "out.part.mp4")
			_, err := Run(context.Background(), copyPlan(transcode.ContainerMP4), src, out, Options{})
			require.NoError(t, err)

			r, err := Verify(context.Background(), src, out, standard.Expectation{
				VideoStreams: 1, AudioStreams: 1, VideoCodec: "hevc", DurationMillis: probeInfo(t, src).Format.Duration.Milliseconds(),
			})
			require.NoError(t, err)
			assert.True(t, r.OK, "%v", r.Problems)
		})
	}
}

// The source's video and audio end is still a floor: an output cut short
// of it fails.
func TestAnOutputShortOfTheSourcesVideoAndAudioStillFails(t *testing.T) {
	src := subtitleTailClip(t, "mkv")
	// The source's video and audio, a second and a half of them (-map 0:v -map 0:a -c copy -t 1.5).
	out := testmedia.Make(t, "short.mp4", testmedia.Spec{
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24:duration=1.5", Encoder: "libx265",
			Options: map[string]string{"preset": "ultrafast", "x265-params": "log-level=error"},
		}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440:sample_rate=48000:duration=1.5"}},
	})
	r, err := Verify(context.Background(), src, out, standard.Expectation{
		VideoStreams: 1, AudioStreams: 1, VideoCodec: "hevc", DurationMillis: probeInfo(t, src).Format.Duration.Milliseconds(),
	})
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Contains(t, joined(r.Problems), "want 3")
}
