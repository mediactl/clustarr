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
	"strings"
	"sync"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// logTail keeps the last bytes of FFmpeg's warnings and errors, for the
// TranscodeJob's status.stderrTail (the argv engine kept ffmpeg's stderr).
type logTail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newLogTail(max int) *logTail { return &logTail{max: max} }

func (t *logTail) add(level ffgo.LogLevel, msg string) {
	if level > ffgo.LogWarning {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, msg...)
	if !strings.HasSuffix(msg, "\n") {
		t.buf = append(t.buf, '\n')
	}
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
}

func (t *logTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// install routes FFmpeg's log here for the run, through the process's one
// trampoline (pkg/ffruntime). A worker runs one transcode at a time.
func (t *logTail) install() (restore func()) { return ffruntime.Capture(t.add) }
