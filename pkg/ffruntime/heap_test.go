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
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rssAnon is this process's resident anonymous memory in bytes.
func rssAnon(t *testing.T) int64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "RssAnon:"); ok {
			kb, err := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
			require.NoError(t, err)
			return kb << 10
		}
	}
	t.Fatal("no RssAnon in /proc/self/status")
	return 0
}

// The C heap keeps what was freed below a chunk still in use, as FFmpeg's
// frames and packets leave it; TrimHeap hands those pages back. It measures
// in a child process, whose heap no earlier run has left holes in.
func TestTrimHeapReturnsTheCHeapsFreedPages(t *testing.T) {
	if os.Getenv(trimChildEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTrimHeapReturnsTheCHeapsFreedPages$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), trimChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		require.Contains(t, string(out), "--- PASS: TestTrimHeapReturnsTheCHeapsFreedPages", "%s", out)
		return
	}
	libc, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	require.NoError(t, err, "the native image's C library is glibc")
	var malloc func(uintptr) unsafe.Pointer
	var free func(unsafe.Pointer)
	purego.RegisterLibFunc(&malloc, libc, "malloc")
	purego.RegisterLibFunc(&free, libc, "free")

	runtime.LockOSThread() // one thread, so one arena holds every chunk
	defer runtime.UnlockOSThread()
	const chunk, chunks = 32 << 10, 4096 // 128 MiB, each chunk under the mmap threshold
	ptrs := make([]unsafe.Pointer, chunks+1)
	base := rssAnon(t)
	for i := range ptrs {
		ptrs[i] = malloc(chunk)
		require.NotNil(t, ptrs[i])
		b := unsafe.Slice((*byte)(ptrs[i]), chunk)
		for j := 0; j < chunk; j += 4096 {
			b[j] = 1 // resident
		}
	}
	// The last chunk, carved from the arena's top after the others, stays
	// in use above them, so free cannot shrink the heap back over them.
	pin := ptrs[chunks]
	for _, p := range ptrs[:chunks] {
		free(p)
	}
	held := rssAnon(t)
	require.Greater(t, held-base, int64(96<<20), "glibc still holds the freed 128 MiB")

	require.True(t, TrimHeap(), "glibc has malloc_trim")
	trimmed := rssAnon(t)
	free(pin)
	assert.Greater(t, held-trimmed, int64(96<<20),
		"TrimHeap returned %d MiB of the 128 MiB freed", (held-trimmed)>>20)
}

// trimChildEnv marks the child process TestTrimHeapReturnsTheCHeapsFreedPages
// measures in.
const trimChildEnv = "FFRUNTIME_TRIM_CHILD"

// heapSeams counts trims instead of making them and shortens IdleTrim for
// one test.
func heapSeams(t *testing.T, idle time.Duration) *atomic.Int32 {
	t.Helper()
	resetBusy()
	var trims atomic.Int32
	oldTrim, oldIdle := trimHeap, idleAfter
	trimHeap, idleAfter = func() bool { trims.Add(1); return true }, idle
	t.Cleanup(func() { trimHeap, idleAfter = oldTrim, oldIdle; resetBusy() })
	return &trims
}

// The heap is trimmed once FFmpeg work has been idle for IdleTrim, never
// while any is in flight, and once for a burst.
func TestTheCHeapIsTrimmedOnceFFmpegWorkIsIdle(t *testing.T) {
	seams(t, time.Second, MaxAbandoned)
	trims := heapSeams(t, 30*time.Millisecond)

	encode := Busy() // a transcode's encode, which Do does not run
	for range 3 {    // a burst of probes beside it
		require.NoError(t, Do(context.Background(), func(context.Context) error { return nil }))
	}
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, trims.Load(), "no trim while an encode is in flight")

	encode()
	encode() // a second call does not count again
	assert.Eventually(t, func() bool { return trims.Load() == 1 }, time.Second, 5*time.Millisecond,
		"the heap is trimmed once the last work ends")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), trims.Load(), "and once for the burst")
}

// Work that starts inside IdleTrim puts the trim off until IdleTrim after
// it ends, even when it ends inside the first idle's window: one trim, at
// the end.
func TestWorkInsideIdleTrimPutsTheTrimOff(t *testing.T) {
	seams(t, time.Second, MaxAbandoned)
	const idle = 300 * time.Millisecond
	trims := heapSeams(t, idle)

	Busy()() // idle from t0; the first window would trim at t0+300ms
	time.Sleep(idle / 2)
	Busy()()             // brief work at t0+150ms: idle again, trim at t0+450ms
	time.Sleep(idle / 2) // t0+300ms
	time.Sleep(idle / 6) // t0+350ms: past the first window, short of the second
	assert.Zero(t, trims.Load(), "the first idle's trim was put off by the work inside it")
	assert.Eventually(t, func() bool { return trims.Load() == 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(idle)
	assert.Equal(t, int32(1), trims.Load(), "one trim for the two idles")

	next := Busy() // and no trim while work is in flight, however long
	time.Sleep(2 * idle)
	assert.Equal(t, int32(1), trims.Load())
	next()
	assert.Eventually(t, func() bool { return trims.Load() == 2 }, time.Second, 5*time.Millisecond)
}

// An abandoned call is still running FFmpeg: it holds the trim off until it
// returns.
func TestAnAbandonedCallHoldsTheTrimOffUntilItReturns(t *testing.T) {
	seams(t, 10*time.Millisecond, MaxAbandoned)
	trims := heapSeams(t, 20*time.Millisecond)

	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorIs(t, Do(ctx, func(context.Context) error { <-release; return nil }), ErrAbandoned)
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, trims.Load(), "no trim while the abandoned call runs")
	close(release)
	assert.Eventually(t, func() bool { return trims.Load() == 1 }, time.Second, 5*time.Millisecond)
}
