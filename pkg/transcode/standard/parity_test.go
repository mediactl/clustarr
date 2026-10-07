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

package standard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// clip makes a 2 s file with ffmpeg, skipping without ffmpeg or libx265.
func clip(t *testing.T, name string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	meta := filepath.Join(dir, "chapters.ffmeta")
	require.NoError(t, os.WriteFile(meta, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1000\ntitle=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=1000\nEND=2000\ntitle=Two\n"), 0o644))
	out := filepath.Join(dir, name)
	a := append([]string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-i", meta, "-map", "0", "-map", "1", "-map_chapters", "2", "-c:a", "aac",
	}, args...)
	if b, err := exec.Command("ffmpeg", append(a, out)...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make the clip: %v\n%s", err, b)
	}
	return out
}

// The controller plans from the stored probe summary and the worker from a
// live probe of the same file; both must reach the same decision and the
// same plan hash, or every job is refused or re-planned at the worker.
func TestTheSummaryAndTheProbePlanTheSameStandard(t *testing.T) {
	for name, c := range map[string]struct {
		src  func() string
		want Decision
	}{
		"compliant mkv": {func() string {
			return clip(t, "c.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error")
		}, DecisionCopyVideo}, // remuxed into MP4
		"h264 mkv": {func() string { return clip(t, "h.mkv", "-c:v", "libx264", "-preset", "ultrafast") }, DecisionEncode},
		"compliant mp4": {func() string {
			return clip(t, "c.mp4", "-pix_fmt", "yuv420p10le", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error", "-tag:v", "hvc1")
		}, DecisionSkip}, // already the MP4 layout
	} {
		t.Run(name, func(t *testing.T) {
			src := c.src()
			mi, raw, err := ffprobeexec.Probe(context.Background(), src)
			require.NoError(t, err)
			live, err := transcode.FromProbe(mi, raw)
			require.NoError(t, err)
			stored, err := transcode.FromSummary(src, mi)
			require.NoError(t, err)

			fromProbe, fromSummary := Plan(live, profile, cpu), Plan(stored, profile, cpu)
			assert.Equal(t, c.want, fromSummary.Decision, fromSummary.Reason)
			assert.Equal(t, fromProbe.Decision, fromSummary.Decision)
			assert.Equal(t, fromProbe.Hash(), fromSummary.Hash())
		})
	}
}

func TestContainerNamesFromTheSummaryAndTheDemuxer(t *testing.T) {
	for name, want := range map[string]transcode.Container{
		"matroska,webm": transcode.ContainerMKV, "mkv": transcode.ContainerMKV, "webm": transcode.ContainerMKV,
		"mov,mp4,m4a,3gp,3g2,mj2": transcode.ContainerMP4, "mp4": transcode.ContainerMP4, "m4v": transcode.ContainerMP4, "mov": transcode.ContainerMP4,
		"mpegts": "", "avi": "", "": "",
	} {
		assert.Equal(t, want, containerOf(name), name)
	}
}

// Sidecar names read a subtitle's language, forced and SDH flags and
// title, so the stored summary must carry them as the live probe does:
// both plan a "Signs" ASS track, a forced SRT, an SDH SRT and a plain one
// alike (Review Focus 5 of the MP4 standard's phase 1 plan).
func TestTheSummaryAndTheProbePlanSubtitlesAlike(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	ass := filepath.Join(dir, "s.ass")
	require.NoError(t, os.WriteFile(ass, []byte("[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n"+
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"+
		"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n"+
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"+
		"Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Sign\n"), 0o644))
	srt := filepath.Join(dir, "s.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\nHello\n"), 0o644))
	out := filepath.Join(dir, "subs.mkv")
	b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-i", ass, "-i", srt, "-i", srt, "-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-map", "3", "-map", "4", "-map", "5",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s:0", "copy", "-c:s:1", "srt", "-c:s:2", "srt", "-c:s:3", "srt",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=Signs",
		"-metadata:s:s:1", "language=ger", "-disposition:s:1", "forced",
		"-metadata:s:s:2", "language=eng", "-disposition:s:2", "hearing_impaired",
		"-metadata:s:s:3", "language=spa", out).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot make the clip: %v\n%s", err, b)
	}
	mi, raw, err := ffprobeexec.Probe(context.Background(), out)
	require.NoError(t, err)
	live, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	stored, err := transcode.FromSummary(out, mi)
	require.NoError(t, err)
	fromProbe, fromSummary := Plan(live, profile, cpu), Plan(stored, profile, cpu)
	require.Len(t, fromSummary.Sidecars, 4, "%+v", fromSummary.Sidecars)
	assert.Equal(t, fromProbe.Sidecars, fromSummary.Sidecars)
	assert.Equal(t, fromProbe.Subtitles, fromSummary.Subtitles)
	assert.Equal(t, fromProbe.Hash(), fromSummary.Hash())
}
