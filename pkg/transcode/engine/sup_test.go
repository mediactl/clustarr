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
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// pgsSegment is one Presentation Graphic Stream segment: "PG", a 90 kHz
// presentation timestamp, a zero decoding timestamp, the type and the
// payload behind its length.
func pgsSegment(pts uint32, typ byte, payload []byte) []byte {
	b := []byte{'P', 'G'}
	b = binary.BigEndian.AppendUint32(b, pts)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, typ)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload))) //nolint:gosec // a test's payload
	return append(b, payload...)
}

// pgsSup is a two-second PGS stream of one display set -- a 2x2 object in
// palette colour 1 at (10,10) on a 320x180 screen, shown from 1 s -- and a
// clearing set at 2 s, as FFmpeg's sup demuxer reads it. FFmpeg encodes no
// PGS, so a test stream is written by hand.
func pgsSup() []byte {
	var out []byte
	// Presentation composition: screen, frame rate, composition 0, epoch
	// start, no palette update, palette 0, one object at (10,10).
	pcs := []byte{
		0x01, 0x40, 0x00, 0xb4, 0x10, 0x00, 0x00, 0x80, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x0a, 0x00, 0x0a,
	}
	// Window 0 at (10,10), 2x2.
	wds := []byte{0x01, 0x00, 0x00, 0x0a, 0x00, 0x0a, 0x00, 0x02, 0x00, 0x02}
	// Palette 0: entry 0 transparent, entry 1 opaque white.
	pds := []byte{0x00, 0x00, 0x00, 0x00, 0x80, 0x80, 0x00, 0x01, 0xeb, 0x80, 0x80, 0xff}
	// Object 0, first and last fragment: 2x2, run-length "two pixels of
	// colour 1, end of line" twice.
	rle := []byte{0x01, 0x01, 0x00, 0x00, 0x01, 0x01, 0x00, 0x00}
	ods := []byte{0x00, 0x00, 0x00, 0xc0}
	n := 4 + len(rle)
	ods = append(ods, byte(n>>16), byte(n>>8), byte(n), 0x00, 0x02, 0x00, 0x02)
	ods = append(ods, rle...)
	// The clearing set: composition 1, normal case, no objects.
	clear := []byte{0x01, 0x40, 0x00, 0xb4, 0x10, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
	segments := [][]byte{
		pgsSegment(90000, 0x16, pcs),
		pgsSegment(90000, 0x17, wds),
		pgsSegment(90000, 0x14, pds),
		pgsSegment(90000, 0x15, ods),
		pgsSegment(90000, 0x80, nil),
		pgsSegment(180000, 0x16, clear),
		pgsSegment(180000, 0x17, wds),
		pgsSegment(180000, 0x80, nil),
	}
	for _, s := range segments {
		out = append(out, s...)
	}
	return out
}

// pgsClip is a 3 s clip with an English PGS track and no text subtitle:
// a Blu-ray whose only subtitle is its image one.
func pgsClip(t *testing.T) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	sup := filepath.Join(dir, "en.sup")
	require.NoError(t, os.WriteFile(sup, pgsSup(), 0o644))
	out := filepath.Join(dir, "pgs.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3",
		"-i", sup,
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-preset", "veryfast", "-c:a", "aac", "-c:s", "copy",
		"-metadata:s:s:0", "language=eng", out)
	return out
}

// An image subtitle no text track covers goes beside the MP4 as a .sup
// (standard.Version 8, owner 2026-10-27: every output is MP4, which carries
// no PGS; the OCR to come reads the sidecar), copied as it is through the
// sup muxer, and the MP4 carries no subtitle stream.
func TestAnUncoveredPGSTrackGoesBesideTheMP4AsASupSidecar(t *testing.T) {
	src := pgsClip(t)
	info := probeInfo(t, src)
	require.Len(t, info.Subtitles, 1)
	require.True(t, info.Subtitles[0].Bitmap, "the probe reads PGS as an image subtitle")

	plan := standard.Plan(info, standard.Profile{Name: "p", Hash: "h", Quality: 24, Container: transcode.ContainerMP4},
		standard.Hardware{Tier: transcode.TierCPUx265})
	require.NotEqual(t, standard.DecisionSkip, plan.Decision, plan.Reason)
	assert.Equal(t, transcode.ContainerMP4, plan.Container)
	assert.Empty(t, plan.Subtitles)
	require.Len(t, plan.Sidecars, 1, "%+v", plan.Dropped)
	assert.Equal(t, standard.SidecarPlan{SourceIndex: 0, Format: standard.SidecarSUP, Codec: "hdmv_pgs_subtitle", Suffix: "en.sup"}, plan.Sidecars[0])

	out := filepath.Join(t.TempDir(), "o.mp4")
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	rep, err := Verify(context.Background(), src, out, plan.Expect)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)

	sup := filepath.Join(filepath.Dir(out), "o.en.sup")
	require.FileExists(t, sup)
	p := ffprobeJSON(t, sup)
	require.Len(t, p.Streams, 1)
	assert.Equal(t, "hdmv_pgs_subtitle", p.Streams[0].CodecName, "the PGS track as it was, in a stream of its own")
	packets := strings.Fields(run(t, "ffprobe", "-v", "error", "-show_entries", "packet=pts_time", "-of", "csv=p=0", sup))
	assert.NotEmpty(t, packets, "the sidecar carries the track's packets")
	for _, s := range ffprobeJSON(t, out).Streams {
		assert.NotEqual(t, "subtitle", s.CodecType, "the MP4 carries no subtitle stream")
	}
	assert.Equal(t, "encode", ffprobeJSON(t, out).Format.Tags["CLUSTARR_VIDEO"], "the output says its video is clustarr's encode")
}
