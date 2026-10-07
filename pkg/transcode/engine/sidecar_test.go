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

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

const assFixture = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n" +
	"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
	"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n" +
	"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello\n"

// An ASS stream copy-muxes into FFmpeg's ass muxer: the sidecar path (spec §4).
func TestAnASSStreamCopyMuxesToAnASSFile(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "in.ass")
	require.NoError(t, os.WriteFile(ass, []byte(assFixture), 0o644))
	src := filepath.Join(dir, "ass.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2", "-i", ass,
		"-c:v", "libx264", "-preset", "veryfast", "-c:s", "copy", src)
	d, err := ffgo.NewDecoder(src)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	var sub *ffgo.StreamInfo
	for _, s := range d.Streams() {
		if s.Type == ffgo.MediaTypeSubtitle {
			sub = s
		}
	}
	require.NotNil(t, sub)
	out := filepath.Join(dir, "out.ass")
	sink, err := newMuxSidecar(out, "ass", sub)
	require.NoError(t, err)
	for {
		p, err := d.ReadPacket()
		require.NoError(t, err)
		if p == nil {
			break
		}
		if p.StreamIndex() == sub.Index {
			require.NoError(t, sink.write(p, sub.TimeBase))
		}
	}
	require.NoError(t, sink.close())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(b), "[Script Info]")
	assert.Contains(t, string(b), "Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello")
}

func TestPlainText(t *testing.T) {
	for _, tc := range []struct{ codec, in, want string }{
		{"text", "<i>Hello</i>\r\nthere", "Hello\nthere"},
		{"text", "{\\an8}<font color=\"#ff0\">Top</font>", "Top"},
		{"webvtt", "<v Bob>Hi &amp; bye</v>", "Hi & bye"},
		{"webvtt", "<c.yellow>Warn</c>", "Warn"},
		{"mov_text", "\x00\x05Hello", "Hello"},
		{"mov_text", "\x00\x02Hi\x00\x00\x00\x0cstyl\x00\x00\x00\x00", "Hi"},
		{"text", "AT&T", "AT&T"},
	} {
		assert.Equal(t, tc.want, plainText(tc.codec, []byte(tc.in)), "%s %q", tc.codec, tc.in)
	}
}

func TestSRTSidecarFormatsCues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.en.forced.srt")
	s := newSRTSidecar(path, "webvtt")
	tb := ffgo.NewRational(1, 1000)
	s.add(500, 1000, tb, []byte("<c.yellow>Sign</c>"))
	s.add(3_723_004, 1500, tb, []byte("Two\r\nlines"))
	s.add(5000, 1000, tb, []byte("   ")) // an empty cue is left out
	require.NoError(t, s.close())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "1\n00:00:00,500 --> 00:00:01,500\nSign\n\n2\n01:02:03,004 --> 01:02:04,504\nTwo\nlines\n\n", string(b))
}

// A run writes every subtitle beside its output (spec §4.1): an ASS track
// copied out to .ass, a SubRip one through the srt muxer (the ffgo
// `-map 0:s:0 out.srt`), a WebVTT one converted; the MP4 carries none; a
// failed run leaves none.
func TestTheEngineWritesTheSidecarsBesideItsOutput(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "in.ass")
	require.NoError(t, os.WriteFile(ass, []byte(assFixture), 0o644))
	srt := filepath.Join(dir, "forced.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\n<i>Sign</i>\n"), 0o644))
	vtt := filepath.Join(dir, "es.vtt")
	require.NoError(t, os.WriteFile(vtt, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<c.yellow>Hola</c>\n"), 0o644))
	in := filepath.Join(dir, "in.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3", "-i", ass, "-i", srt, "-i", vtt,
		"-map", "0", "-map", "1", "-map", "2", "-map", "3",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error",
		"-c:s:0", "copy", "-c:s:1", "srt", "-c:s:2", "webvtt",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=ger", "-metadata:s:s:2", "language=spa",
		"-disposition:s:1", "forced", in)
	plan := standard.Plan(probeInfo(t, in), standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Len(t, plan.Sidecars, 3, "%+v", plan.Sidecars)
	out := filepath.Join(dir, "Film.part-ab12cd34-1.mp4")
	_, err := Run(context.Background(), plan, in, out, Options{})
	require.NoError(t, err)
	got := map[string]string{}
	for _, s := range plan.Sidecars {
		b, err := os.ReadFile(fsops.SidecarPath(out, s.Suffix))
		require.NoError(t, err, s.Suffix)
		got[s.Suffix] = string(b)
	}
	assert.Contains(t, got["en.ass"], "Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello")
	assert.Contains(t, got["de.forced.srt"], "00:00:00,500 --> 00:00:01,500\n<i>Sign</i>", "SubRip is copied as ffmpeg -map 0:s:0 out.srt writes it")
	assert.Contains(t, got["es.srt"], "00:00:01,000 --> 00:00:02,000\nHola")
	for _, s := range ffprobeJSON(t, out).Streams {
		assert.NotEqual(t, "subtitle", s.CodecType, "the MP4 carries no subtitle stream")
	}

	// A cancelled run removes its sidecars with its output.
	ctx, cancel := context.WithCancel(context.Background())
	out2 := filepath.Join(dir, "Film.part-ab12cd34-2.mp4")
	_, err = Run(ctx, plan, in, out2, Options{Progress: func(transcode.Progress) { cancel() }, ProgressEvery: 1})
	require.Error(t, err)
	for _, s := range plan.Sidecars {
		_, err := os.Stat(fsops.SidecarPath(out2, s.Suffix))
		assert.ErrorIs(t, err, os.ErrNotExist, s.Suffix)
	}
}
