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

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// hevcClip is a 2 s HEVC Main 10 clip with an AAC track, in Matroska, its
// video encoded with x265's own params (log-level=error when none).
func hevcClip(t *testing.T, params ...string) string {
	t.Helper()
	return hevcClipAs(t, "src.mkv", "", params...)
}

// hevcClipAs is hevcClip under name, in the container format names (the
// name's own when empty).
func hevcClipAs(t *testing.T, name, format string, params ...string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	x265 := "log-level=error"
	if len(params) > 0 {
		x265 = params[0]
	}
	return testmedia.Make(t, name, testmedia.Spec{
		Format: format,
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24:duration=2", Encoder: "libx265", PixFmt: "yuv420p10le",
			Options: map[string]string{"preset": "ultrafast", "x265-params": x265},
		}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440:sample_rate=48000:duration=2"}},
	})
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
	return videoProps(t, path)
}

func TestCopiedStreamsLeaveTheSourceContainersTagsBehind(t *testing.T) {
	for name, src := range map[string]func() string{
		"mp4 to mkv": func() string { // `-tag:v hvc1`
			return testmedia.Remux(t, hevcClip(t), "src.mp4", testmedia.Retag{CodecTags: map[int]string{0: "hvc1"}})
		},
		"ts to mkv": func() string { return hevcClipAs(t, "src.ts", "mpegts") },
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
		_, err := Run(context.Background(), copyPlan(transcode.ContainerMP4), hevcClip(t), out, Options{})
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
	_, err := Run(context.Background(), plan, hevcClip(t), out, Options{})
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

// HEVC in MP4 is tagged hvc1, its parameter sets in the sample entry --
// unless the source's configuration record carries none, when a copy is
// tagged hev1, the entry that lets them travel in the stream: an hvc1 file
// of such a source is one FFmpeg cannot open (Black Mirror S05's NF
// encodes, 2026-10-16: a 23-byte hvcC, every VPS, SPS and PPS in-band).
func TestHEVCInMP4IsTaggedForWhereItsParameterSetsTravel(t *testing.T) {
	noArrays := append(append([]byte{1}, make([]byte, 21)...), 0)   // hvcC, numOfArrays 0
	withArrays := append(append([]byte{1}, make([]byte, 21)...), 3) // VPS, SPS, PPS
	annexB := []byte{0, 0, 0, 1, 0x40, 0x01, 0x0c}                  // a start code
	for _, tt := range []struct {
		name      string
		codec     string
		container transcode.Container
		extradata []byte
		want      uint32
	}{
		{"parameter sets in the record", "hevc", transcode.ContainerMP4, withArrays, hvc1},
		{"Annex B extradata, converted by the muxer", "hevc", transcode.ContainerMP4, annexB, hvc1},
		{"a record with no parameter sets", "hevc", transcode.ContainerMP4, noArrays, hev1},
		{"no extradata at all", "hevc", transcode.ContainerMP4, nil, hev1},
		{"Matroska takes its own", "hevc", transcode.ContainerMKV, noArrays, 0},
		{"H.264 takes the muxer's", "h264", transcode.ContainerMP4, nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, copyTag(tt.codec, tt.container, tt.extradata))
		})
	}
}
