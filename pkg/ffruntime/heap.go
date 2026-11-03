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

package ffruntime

import (
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

// FFmpeg's buffers live in the C heap, which the Go collector never sees,
// and glibc keeps what a thread freed in that thread's arena for its next
// malloc rather than return it. Each OS thread that calls into C -- the Go
// threads purego runs on, the GPU driver's own -- gets an arena of its own,
// so a process that runs FFmpeg for hours holds ever more freed memory: on
// talos-88 (2026-11-03) four hevc_qsv jobs left a transcode pool worker at
// 757 MiB RSS with 2 MiB of the C heap in use, and one malloc_trim(0) took
// it to 301 MiB in 50 ms. So the process trims its C heap whenever its
// FFmpeg work has been idle for IdleTrim. (The native image also caps the
// arenas at two, MALLOC_ARENA_MAX; that bounds the growth, this returns it.)

// IdleTrim is how long no FFmpeg work may be in flight (Do, Busy) before
// the C heap is trimmed: long enough that a burst -- an import's season
// pack of probes, a season's segment decodes -- trims once, at its end.
const IdleTrim = 2 * time.Second

var (
	trimOnce   sync.Once
	mallocTrim func(pad uintptr) int32

	busyMu    sync.Mutex
	busy      int
	idleGen   uint64
	idleTimer *time.Timer
	idleAfter = IdleTrim
	trimHeap  = TrimHeap
)

// TrimHeap hands every free page of the C heap back to the kernel through
// glibc's malloc_trim(0), and reports whether it could: false where the C
// library has none (not glibc). It frees nothing in use, and it is safe
// beside running FFmpeg calls -- glibc trims each arena under that arena's
// lock -- though a call running then may wait a few milliseconds for it.
func TrimHeap() bool {
	trimOnce.Do(func() {
		lib, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		sym, err := purego.Dlsym(lib, "malloc_trim")
		if err != nil {
			return
		}
		purego.RegisterFunc(&mallocTrim, sym)
	})
	if mallocTrim == nil {
		return false
	}
	mallocTrim(0)
	return true
}

// Busy marks FFmpeg work in flight until the func it returns is called;
// Do marks its own calls. Once the last work in flight ends and none starts
// for IdleTrim, the C heap is trimmed (TrimHeap). The func may be called
// more than once; only the first call counts.
func Busy() (done func()) {
	busyMu.Lock()
	busy++
	idleGen++
	if idleTimer != nil {
		idleTimer.Stop()
		idleTimer = nil
	}
	busyMu.Unlock()
	var once sync.Once
	return func() { once.Do(endBusy) }
}

func endBusy() {
	busyMu.Lock()
	defer busyMu.Unlock()
	busy--
	if busy > 0 {
		return
	}
	idleGen++
	gen := idleGen
	idleTimer = time.AfterFunc(idleAfter, func() {
		busyMu.Lock()
		idle := busy == 0 && idleGen == gen
		if idle {
			idleTimer = nil
		}
		busyMu.Unlock()
		if idle {
			trimHeap()
		}
	})
}

// resetBusy is for this package's tests only.
func resetBusy() {
	busyMu.Lock()
	defer busyMu.Unlock()
	if idleTimer != nil {
		idleTimer.Stop()
	}
	busy, idleGen, idleTimer = 0, 0, nil
}
