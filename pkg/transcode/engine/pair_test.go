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
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dashPair writes what a trailer's source hands out: one fragmented MP4
// with the video alone and one with the audio alone, seconds long.
func dashPair(t *testing.T, seconds int) (video, audio string) {
	t.Helper()
	realProber(t)
	dir := t.TempDir()
	video, audio = filepath.Join(dir, "clip.137.mp4"), filepath.Join(dir, "clip.140.m4a")
	d := strconv.Itoa(seconds)
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration="+d,
		"-c:v", "libx264", "-preset", "ultrafast", "-bf", "2", "-an",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof", video)
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+d,
		"-c:a", "aac", "-b:a", "96k", "-vn",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", audio)
	return video, audio
}

// A trailer's video-only and audio-only files become one MP4 with both
// streams copied, as long as its inputs, its index ahead of its media.
func TestMuxPairCopiesBothStreamsIntoOneFastStartMP4(t *testing.T) {
	video, audio := dashPair(t, 4)
	out := filepath.Join(t.TempDir(), "trailer.mp4")
	require.NoError(t, MuxPair(context.Background(), video, audio, out))

	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 2)
	assert.Equal(t, "video", p.Streams[0].CodecType)
	assert.Equal(t, "h264", p.Streams[0].CodecName, "copied, not re-encoded")
	assert.Equal(t, "audio", p.Streams[1].CodecType)
	assert.Equal(t, "aac", p.Streams[1].CodecName, "copied, not re-encoded")
	info := probeInfo(t, out)
	assert.InDelta(t, 4.0, info.Format.Duration.Seconds(), 0.2)

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
