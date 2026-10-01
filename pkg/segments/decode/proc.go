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

package decode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// maxOutput bounds what one ffmpeg run may write: ten minutes of samples
// is 13 MB, 900 end frames 8 MB.
const maxOutput = 64 << 20

// stderrTail is how much of ffmpeg's stderr an error carries.
const stderrTail = 2048

// run executes ffmpeg with args after -nostdin and the thread count, and
// returns its stdout. ffmpeg runs in its own process group, killed whole
// when ctx ends -- a child holding the pipe open would otherwise keep Wait
// blocked -- and Wait gives up 5 s after that (CLAUDE.md).
func (d Decoder) run(ctx context.Context, args ...string) ([]byte, error) {
	full := []string{"-nostdin", "-v", "error"}
	if d.Threads > 0 {
		full = append(full, "-threads", strconv.Itoa(d.Threads))
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, d.FFmpeg, full...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	var stderr tail
	cmd.Stdout = &limited{w: &stdout, n: maxOutput}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("decode: ffmpeg: %w", ctx.Err())
		}
		return nil, fmt.Errorf("decode: ffmpeg: %w: %s", err, bytes.TrimSpace(stderr.b))
	}
	return stdout.Bytes(), nil
}

// tail keeps the last stderrTail bytes written.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > stderrTail {
		t.b = t.b[len(t.b)-stderrTail:]
	}
	return len(p), nil
}

var errTooLarge = errors.New("decode: ffmpeg output over the cap")

// limited fails a write past n bytes, which ends ffmpeg with a broken pipe.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errTooLarge
	}
	l.n -= len(p)
	return l.w.Write(p)
}
