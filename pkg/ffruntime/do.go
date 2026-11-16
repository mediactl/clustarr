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
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	doMu         sync.Mutex
	outstanding  int
	wedged       = make(chan struct{})
	wedgedOnce   = new(sync.Once)
	grace        = Grace
	maxAbandoned = MaxAbandoned
	panics       atomic.Int64
)

// Do runs f on its own goroutine. When ctx ends and f has not returned
// within Grace, Do returns an error wrapping ErrAbandoned and ctx.Err() and
// leaves f running; Abandoned counts it until it returns. At MaxAbandoned
// outstanding calls Wedged closes and Do refuses every call with ErrWedged.
// A caller records an abandoned result as non-transient (spec §6.6), except
// markers', which retries the season after a delay: its abandonments are NFS
// stalls (app/segments/worker; §6.6 as amended 2026-10-15).
//
// A panic in f is recovered and returned as a *PanicError (ErrPanicked),
// and counted (Panics). f runs on Do's own goroutine, out of reach of any
// recover its caller holds -- the bus's handler recover among them -- so an
// unrecovered panic in a binding killed the whole process: on 2026-11-11 one
// FLAC's 4,216-byte lyrics tag (ffgo's goString, fixed in
// v0.0.0-clustarr.17) took the import agent down at every delivery of the
// music root's scan until the scan's attempts ran out. f's own defers run
// before the recover, so what it opened is closed; a binding left holding a
// lock wedges the next call, which Do abandons and, at MaxAbandoned, the
// liveness check restarts. A fault in C code (a segfault) is no Go panic and
// still ends the process.
func Do(ctx context.Context, f func(context.Context) error) error {
	select {
	case <-Wedged():
		return ErrWedged
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	end := Busy() // until f returns, abandoned or not
	go func() {
		defer end()
		done <- call(ctx, f)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
	}
	abandon(done)
	return fmt.Errorf("%w: %w", ErrAbandoned, ctx.Err())
}

// call runs f, recovering a panic in it as a *PanicError.
func call(ctx context.Context, f func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			panics.Add(1)
			err = &PanicError{Value: r, Frame: panicFrame(), Stack: debug.Stack()}
		}
	}()
	return f(ctx)
}

// PanicError is a panic Do recovered from an FFmpeg call. It wraps
// ErrPanicked, and Value when Value is an error (a runtime.Error for a
// binding's out-of-range slice).
type PanicError struct {
	// Value is what was passed to panic.
	Value any
	// Frame is the function that panicked and its file:line, the first
	// frame below the panic outside the runtime; empty when none was found.
	Frame string
	// Stack is the panicking goroutine's stack, for a caller to log.
	Stack []byte
}

// Error names the panic and where it happened, short enough for a status
// message; the whole stack is in Stack.
func (e *PanicError) Error() string {
	if e.Frame == "" {
		return fmt.Sprintf("%v: %v", ErrPanicked, e.Value)
	}
	return fmt.Sprintf("%v: %v (at %s)", ErrPanicked, e.Value, e.Frame)
}

// Unwrap is ErrPanicked, and the panic's value when it is an error.
func (e *PanicError) Unwrap() []error {
	if v, ok := e.Value.(error); ok {
		return []error{ErrPanicked, v}
	}
	return []error{ErrPanicked}
}

// panicFrame is called from call's deferred recover while the panicking
// stack is still in place: the first frame below runtime.gopanic that is
// not the runtime's own (runtime.goPanicSliceAcap and the like sit between
// them for a runtime error).
func panicFrame() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	below := false
	for {
		fr, more := frames.Next()
		switch {
		case fr.Function == "runtime.gopanic":
			below = true
		case below && !strings.HasPrefix(fr.Function, "runtime."):
			return fmt.Sprintf("%s (%s:%d)", fr.Function, filepath.Base(fr.File), fr.Line)
		}
		if !more {
			return ""
		}
	}
}

// Panics is how many FFmpeg calls have panicked in this process, each
// recovered by Do.
func Panics() int64 { return panics.Load() }

func abandon(done <-chan error) {
	doMu.Lock()
	outstanding++
	if outstanding >= maxAbandoned {
		w, once := wedged, wedgedOnce
		once.Do(func() { close(w) })
	}
	doMu.Unlock()
	go func() {
		<-done
		doMu.Lock()
		outstanding--
		doMu.Unlock()
	}()
}

// Abandoned is how many abandoned calls are still running.
func Abandoned() int {
	doMu.Lock()
	defer doMu.Unlock()
	return outstanding
}

// Wedged closes once MaxAbandoned calls are outstanding; it never reopens.
func Wedged() <-chan struct{} {
	doMu.Lock()
	defer doMu.Unlock()
	return wedged
}

// Healthz is the process-level healthz check "ffgo" (spec §3.3): an error
// once wedged, so the kubelet restarts a pod a restart can cure.
func Healthz(*http.Request) error {
	select {
	case <-Wedged():
		return ErrWedged
	default:
		return nil
	}
}

// resetDo is for this package's tests only.
func resetDo() {
	doMu.Lock()
	defer doMu.Unlock()
	outstanding, wedged, wedgedOnce = 0, make(chan struct{}), new(sync.Once)
}
