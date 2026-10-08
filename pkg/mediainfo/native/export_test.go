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

package native

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testProber is a Prober for a test, which skips without FFmpeg 9 and the
// shim. T2a's test/nativetest (W7.3) replaces the skip with
// nativetest.FFmpeg(t, Needs), which fails instead where FFmpeg is required.
func testProber(t *testing.T) *Prober {
	t.Helper()
	p, err := New()
	if err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	return p
}

// ffmpegClip makes a clip with the ffmpeg CLI. W6.19 converts every caller
// to internal/testmedia, which makes clips on ffgo.
func ffmpegClip(t *testing.T, name string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make the clip")
	}
	out := filepath.Join(t.TempDir(), name)
	a := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	b, err := exec.Command("ffmpeg", append(a, out)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", b)
	return out
}
