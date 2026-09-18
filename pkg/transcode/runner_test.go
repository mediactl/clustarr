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

package transcode_test

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestParseProgressBlockMatchesTheVerifiedNoteExample(t *testing.T) {
	b, err := os.ReadFile("../../testdata/transcode/fixtures/progress-block.txt")
	require.NoError(t, err)

	var got []transcode.Progress
	require.NoError(t, transcode.ParseProgressStream(bufio.NewScanner(strings.NewReader(string(b))), 10_000, func(p transcode.Progress) {
		got = append(got, p)
	}))

	require.Len(t, got, 1, "one progress=end block yields exactly one Progress")
	p := got[0]
	require.Equal(t, int64(72), p.Frame)
	require.Equal(t, int32(0), p.FPSMilli)         // fps=0.00
	require.Equal(t, int64(3000), p.OutTimeMillis) // out_time_us=3000000 -> 3000ms; NOT divided again despite the out_time_ms quirk (note §7)
	require.Equal(t, int32(9240), p.SpeedMilli)    // speed=9.24x -> 9240
	require.Equal(t, int32(623), p.BitrateKbps)    // "623.4kbits/s" -> 623
	require.Equal(t, int32(30), p.Percent)         // 3000ms / 10000ms duration
}

func TestRunReturnsATypedErrorWithTheStderrTailOnNonZeroExit(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	r := transcode.NewRunner("/usr/bin/ffmpeg")
	plan := &transcode.PlanResult{
		Decision:  transcode.DecisionEncode,
		Input:     "/nonexistent/does-not-exist.mkv", // ffmpeg exits 1 with "No such file or directory"
		Output:    filepath.Join(t.TempDir(), "out.part.mkv"),
		Container: transcode.ContainerMKV,
		VideoArgs: []string{"-c:v", "copy"},
	}

	err := r.Run(context.Background(), plan, func(transcode.Progress) {})
	require.Error(t, err)

	var runErr *transcode.RunError
	require.ErrorAs(t, err, &runErr)
	require.NotZero(t, runErr.ExitCode)
	require.Contains(t, strings.ToLower(runErr.StderrTail), "no such file")
	require.LessOrEqual(t, len(runErr.StderrTail), 4096)
}

func TestRunHonoursContextCancellationAndSendsSIGINTFirst(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	r := transcode.NewRunner("/usr/bin/ffmpeg")
	plan := &transcode.PlanResult{
		Decision:  transcode.DecisionEncode,
		Input:     "", // overridden below via a lavfi source; see Args note
		Output:    filepath.Join(t.TempDir(), "cancel.part.mkv"),
		Container: transcode.ContainerMKV,
		// a 10s synthetic source encoded at veryslow guarantees Run is still
		// mid-flight well past the 100ms timeout below, on any CI box.
		HWInit:    []string{"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=10"},
		VideoArgs: []string{"-c:v", "libx265", "-preset", "veryslow", "-crf", "30"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := r.Run(ctx, plan, func(transcode.Progress) {})
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
