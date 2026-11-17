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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// dashPair writes what a trailer's source hands out: one fragmented MP4
// with the video alone and one with the audio alone, seconds long.
func dashPair(t *testing.T, seconds int) (video, audio string) {
	t.Helper()
	fragmented := map[string]string{"movflags": "+frag_keyframe+empty_moov+default_base_moof"}
	d := time.Duration(seconds) * time.Second
	video = testmedia.Make(t, "clip.137.mp4", testmedia.Spec{
		Format: "mp4", MuxerOptions: fragmented, Duration: d,
		Video: []testmedia.Video{{Source: "testsrc2=size=320x180:rate=24", Options: map[string]string{"preset": "ultrafast", "bf": "2"}}},
	})
	audio = testmedia.Make(t, "clip.140.m4a", testmedia.Spec{
		Format: "mp4", MuxerOptions: fragmented, Duration: d,
		Audio: []testmedia.Audio{{Source: "sine=frequency=440", BitrateKbps: 96}},
	})
	return video, audio
}

// A trailer's video-only and audio-only files become one MP4 with both
// streams copied, as long as its inputs, its index ahead of its media.
func TestMuxPairCopiesBothStreamsIntoOneFastStartMP4(t *testing.T) {
	video, audio := dashPair(t, 4)
	out := filepath.Join(t.TempDir(), "trailer.mp4")
	require.NoError(t, MuxPair(context.Background(), video, audio, out))

	info := testmedia.Inspect(t, out)
	require.Len(t, info.Streams, 2)
	assert.Equal(t, "video", info.Streams[0].Type)
	assert.Equal(t, "h264", info.Streams[0].Codec, "copied, not re-encoded")
	assert.Equal(t, "audio", info.Streams[1].Type)
	assert.Equal(t, "aac", info.Streams[1].Codec, "copied, not re-encoded")
	assert.InDelta(t, 4.0, info.Duration.Seconds(), 0.2)

	body, err := os.ReadFile(out)
	require.NoError(t, err)
	moov, mdat := indexOfBox(body, "moov"), indexOfBox(body, "mdat")
	require.Positive(t, moov)
	require.Positive(t, mdat)
	assert.Less(t, moov, mdat, "the index leads the media")
}

// A mux that cannot be written leaves no file, and an input without its
// stream is refused before anything is written.
func TestMuxPairRefusesWhatIsNotAPairAndLeavesNoOutput(t *testing.T) {
	video, audio := dashPair(t, 1)
	dir := t.TempDir()

	out := filepath.Join(dir, "swapped.mp4")
	require.Error(t, MuxPair(context.Background(), audio, video, out))
	assert.NoFileExists(t, out)

	require.Error(t, MuxPair(context.Background(), video, audio, filepath.Join(dir, "trailer.mkv")))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out = filepath.Join(dir, "cancelled.mp4")
	require.Error(t, MuxPair(ctx, video, audio, out))
	assert.NoFileExists(t, out)
}

// indexOfBox is where the first top-level-looking box of a type starts.
func indexOfBox(b []byte, typ string) int {
	for i := 4; i+4 <= len(b); i++ {
		if string(b[i:i+4]) == typ {
			return i
		}
	}
	return -1
}
