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
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// hevcClip is a 2 s HEVC Main 10 clip with an AAC track, in a container
// named by ext, made with extra ffmpeg args (a tag, a format).
func hevcClip(t *testing.T, ext string, args ...string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), "src."+ext)
	a := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-pix_fmt", "yuv420p10le", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error",
		"-c:a", "aac",
	}
	a = append(a, args...)
	run(t, "ffmpeg", append(a, out)...)
	return out
}

func copyPlan(c transcode.Container) standard.Result {
	return standard.Result{
		Decision: standard.DecisionCopyVideo, Container: c,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"},
		Audio: []standard.AudioPlan{{SourceIndex: 0, Action: "copy"}},
	}
}

// codecTags is ffprobe's codec name and tag of the output's video.
func codecTags(t *testing.T, path string) string {
	t.Helper()
	return entries(t, path, "-show_entries", "stream=codec_name,codec_tag_string")
}

func TestCopiedStreamsLeaveTheSourceContainersTagsBehind(t *testing.T) {
	for name, src := range map[string]func() string{
		"mp4 to mkv": func() string { return hevcClip(t, "mp4", "-tag:v", "hvc1") },
		"ts to mkv":  func() string { return hevcClip(t, "ts") },
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out.part.mkv")
			_, err := Run(context.Background(), copyPlan(transcode.ContainerMKV), src(), out, Options{})
			require.NoError(t, err)
			p := ffprobeJSON(t, out)
			require.Len(t, p.Streams, 2)
			assert.Equal(t, "hevc", p.Streams[0].CodecName)
			assert.Equal(t, "aac", p.Streams[1].CodecName)
			assert.InDelta(t, 2.0, p.seconds(t), 0.2)
		})
	}
}

func TestHEVCInMP4IsTaggedForApplePlayers(t *testing.T) {
	t.Run("copied", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out.part.mp4")
		_, err := Run(context.Background(), copyPlan(transcode.ContainerMP4), hevcClip(t, "mkv"), out, Options{})
		require.NoError(t, err)
		assert.Contains(t, codecTags(t, out), "codec_tag_string=hvc1")
	})
	t.Run("encoded", func(t *testing.T) {
		src := everythingClip(t, 2)
		plan := encodePlan(cpuVideo("sdr", standard.ColorTags{}))
		plan.Container = transcode.ContainerMP4
		out := filepath.Join(t.TempDir(), "out.part.mp4")
		_, err := Run(context.Background(), plan, src, out, Options{})
		require.NoError(t, err)
		assert.Contains(t, codecTags(t, out), "codec_tag_string=hvc1")
	})
}

func TestMP4KeepsTheProfileTagAndStartsWithItsIndex(t *testing.T) {
	plan := copyPlan(transcode.ContainerMP4)
	plan.Tags = map[string]string{"CLUSTARR_PROFILE": "p@h"}
	out := filepath.Join(t.TempDir(), "out.part.mp4")
	_, err := Run(context.Background(), plan, hevcClip(t, "mkv"), out, Options{})
	require.NoError(t, err)
	assert.Equal(t, "p@h", ffprobeJSON(t, out).Format.Tags["CLUSTARR_PROFILE"])
	assert.Less(t, atomOffset(t, out, "moov"), atomOffset(t, out, "mdat"), "faststart puts the index first")
}

// atomOffset is where a top-level MP4 atom starts, -1 when absent.
func atomOffset(t *testing.T, path, name string) int64 {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	for off := int64(0); off+8 <= int64(len(b)); {
		size := int64(binary.BigEndian.Uint32(b[off:]))
		if bytes.Equal(b[off+4:off+8], []byte(name)) {
			return off
		}
		if size == 1 && off+16 <= int64(len(b)) {
			size = int64(binary.BigEndian.Uint64(b[off+8:]))
		}
		if size < 8 {
			break
		}
		off += size
	}
	return -1
}
