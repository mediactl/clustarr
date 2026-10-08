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
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("ffmpeg is not on PATH")
		}
		t.Skip("ffmpeg is not on PATH")
	}
	out := filepath.Join(t.TempDir(), "clip.mkv")
	cmd := exec.Command(bin, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=10:s=320x240:r=25",
		"-f", "lavfi", "-i", "sine=d=10:f=440", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-shortest", out)
	require.NoError(t, cmd.Run())
	return out
}

func TestAudioDecodesTheWindow(t *testing.T) {
	ready(t)
	pcm, err := decode.Decoder{}.Audio(context.Background(), clip(t), 0, 2, 5)
	require.NoError(t, err)
	assert.InDelta(t, 5*11025, len(pcm), 11025/10, "five seconds of mono 11,025 Hz")
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
