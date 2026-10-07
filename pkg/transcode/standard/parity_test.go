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

	"github.com/mediactl/clustarr/pkg/mediainfo"
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
			mi, raw, err := mediainfo.Probe(context.Background(), src)
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
