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

package decode_test

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/segments/decode"
)

// ready skips without FFmpeg 9 and the ffgo shim (NATIVE_ENV), and fails
// instead under CLUSTARR_REQUIRE_TOOLS=1 (OD44). T2 replaces it with
// nativetest.FFmpeg(t, decode.Needs).
func ready(t *testing.T) {
	t.Helper()
	if err := ffruntime.Require(decode.Needs); err != nil {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
}

// clip makes a 10 s Matroska file with a test picture and a tone, a
// keyframe every second as real releases have one every few. ffmpeg(1)
// only makes the fixture (NATIVE_ENV puts the test-only CLI on PATH); T2
// makes it through testmedia.Make.
func clip(t *testing.T) string {
	t.Helper()
	return makeClip(t, "-f", "lavfi", "-i", "testsrc=d=10:s=320x240:r=25",
		"-f", "lavfi", "-i", "sine=d=10:f=440", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-shortest")
}

// lateAudioClip is clip with its audio starting half a second after the
// picture, as in WEB-DLs whose first audio frame is at 21 ms (2026-10-15).
func lateAudioClip(t *testing.T) string {
	t.Helper()
	return makeClip(t, "-f", "lavfi", "-i", "testsrc=d=10:s=320x240:r=25",
		"-itsoffset", "0.5", "-f", "lavfi", "-i", "sine=d=9.5:f=440", "-c:v", "libx264", "-g", "25", "-c:a", "aac")
}

func makeClip(t *testing.T, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "clip.mkv")
	cmd := exec.Command(ffmpeg(t), append(append([]string{"-v", "error"}, args...), out)...)
	require.NoError(t, cmd.Run())
	return out
}

// ffmpegAudio is the window as ffmpeg(1) decodes it, which Audio ports
// (manager-agent split design, the analysis audio command).
func ffmpegAudio(t *testing.T, path string, fromS, durS float64) []byte {
	t.Helper()
	out, err := exec.Command(ffmpeg(t), "-v", "error", "-ss", strconv.FormatFloat(fromS, 'f', -1, 64),
		"-t", strconv.FormatFloat(durS, 'f', -1, 64), "-i", path,
		"-map", "0:a:0", "-vn", "-sn", "-dn", "-ac", "1", "-ar", "11025", "-f", "s16le", "-").Output()
	require.NoError(t, err)
	return out
}

func ffmpeg(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("ffmpeg is not on PATH")
		}
		t.Skip("ffmpeg is not on PATH")
	}
	return bin
}

func pcmBytes(pcm []int16) []byte {
	b := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func TestAudioDecodesTheWindow(t *testing.T) {
	ready(t)
	pcm, err := decode.Decoder{}.Audio(context.Background(), clip(t), 0, 2, 5)
	require.NoError(t, err)
	assert.InDelta(t, 5*11025, len(pcm), 11025/10, "five seconds of mono 11,025 Hz")
}

// atrim=starti=0 counts its duration from the window's start, not from the
// first sample it passes: a file whose audio starts after its picture
// ended atrim before Audio stopped pushing, and the next push failed with
// AVERROR_EOF -- every start window of 136 files on talos (2026-10-15).
func TestAudioWindowOfLateStartingAudioIsFFmpegs(t *testing.T) {
	ready(t)
	p := lateAudioClip(t)
	pcm, err := decode.Decoder{}.Audio(context.Background(), p, 0, 0, 5)
	require.NoError(t, err)
	assert.Equal(t, ffmpegAudio(t, p, 0, 5), pcmBytes(pcm))
}

func TestFramesOnePerSecondToTheEnd(t *testing.T) {
	ready(t)
	frames, err := decode.Decoder{}.Frames(context.Background(), clip(t), 4)
	require.NoError(t, err)
	assert.InDelta(t, 6, len(frames), 1)
	for _, f := range frames {
		assert.Len(t, f, decode.GrayW*decode.GrayH)
	}
}

func TestFrameIsOneRGBFrame(t *testing.T) {
	ready(t)
	f, err := decode.Decoder{}.Frame(context.Background(), clip(t), 3)
	require.NoError(t, err)
	assert.Len(t, f, decode.RGBW*decode.RGBH*3)
}

func TestAFailureCarriesFFmpegsMessage(t *testing.T) {
	ready(t)
	_, err := decode.Decoder{}.Audio(context.Background(), filepath.Join(t.TempDir(), "missing.mkv"), 0, 0, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No such file")
}

// End frames are sampled from keyframes only: decoding every frame of a
// movie's last 15 minutes took 316 s of HEVC on the owner's cluster
// (2026-10-01), where keyframes are a few hundred decodes. The clip has a
// keyframe a second, so skip_frame=nokey decodes at most those from the
// seek point on.
func TestFramesDecodeKeyframesOnly(t *testing.T) {
	ready(t)
	p := clip(t)
	n, restore := decode.CountDecodedFrames()
	defer restore()
	_, err := decode.Decoder{}.Frames(context.Background(), p, 4)
	require.NoError(t, err)
	assert.LessOrEqual(t, n(), 7, "keyframes from 4 s to 10 s, plus the one before the seek point")
	assert.Positive(t, n())
}
