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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/decode"
)

func ffmpeg(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
	return p
}

// clip makes a 10 s Matroska file with a test picture and a tone, a
// keyframe every second as real releases have one every few.
func clip(t *testing.T, bin string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "clip.mkv")
	cmd := exec.Command(bin, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=10:s=320x240:r=25",
		"-f", "lavfi", "-i", "sine=d=10:f=440", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-shortest", out)
	require.NoError(t, cmd.Run())
	return out
}

func TestAudioDecodesTheWindow(t *testing.T) {
	bin := ffmpeg(t)
	pcm, err := decode.Decoder{FFmpeg: bin}.Audio(context.Background(), clip(t, bin), 0, 2, 5)
	require.NoError(t, err)
	assert.InDelta(t, 5*11025, len(pcm), 11025/10, "five seconds of mono 11,025 Hz")
}

func TestFramesOnePerSecondToTheEnd(t *testing.T) {
	bin := ffmpeg(t)
	frames, err := decode.Decoder{FFmpeg: bin}.Frames(context.Background(), clip(t, bin), 4)
	require.NoError(t, err)
	assert.InDelta(t, 6, len(frames), 1)
	for _, f := range frames {
		assert.Len(t, f, decode.GrayW*decode.GrayH)
	}
}

func TestFrameIsOneRGBFrame(t *testing.T) {
	bin := ffmpeg(t)
	f, err := decode.Decoder{FFmpeg: bin}.Frame(context.Background(), clip(t, bin), 3)
	require.NoError(t, err)
	assert.Len(t, f, decode.RGBW*decode.RGBH*3)
}

// A hung ffmpeg -- here a script whose child holds stdout open -- is killed
// with its process group at the deadline, not waited on until the child
// exits by itself.
func TestAHungFFmpegIsKilledWithItsGroup(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 60 &\nsleep 60\n"), 0o755))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := decode.Decoder{FFmpeg: script}.Audio(ctx, "x.mkv", 0, 0, 1)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 6*time.Second)
}

func TestAFailureCarriesFFmpegsMessage(t *testing.T) {
	bin := ffmpeg(t)
	_, err := decode.Decoder{FFmpeg: bin}.Audio(context.Background(), filepath.Join(t.TempDir(), "missing.mkv"), 0, 0, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No such file")
}

// End frames are sampled from keyframes only: decoding every frame of a
// movie's last 15 minutes took 316 s of HEVC on the owner's cluster
// (2026-10-01), where keyframes are a few hundred decodes.
func TestFramesDecodeKeyframesOnly(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "ffmpeg")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > "+argsFile+"\n"), 0o755))
	_, err := decode.Decoder{FFmpeg: script}.Frames(context.Background(), "movie.mkv", 100)
	require.NoError(t, err)
	b, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	args := string(b)
	assert.Contains(t, args, "-skip_frame nokey -ss 100.000 -i movie.mkv", "keyframes only, set before the input")
}
