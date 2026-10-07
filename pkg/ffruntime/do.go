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
	"sync"
	"time"
)

var (
	doMu         sync.Mutex
	outstanding  int
	wedged       = make(chan struct{})
	wedgedOnce   = new(sync.Once)
	grace        = Grace
	maxAbandoned = MaxAbandoned
)

// Do runs f on its own goroutine. When ctx ends and f has not returned
// within Grace, Do returns an error wrapping ErrAbandoned and ctx.Err() and
// leaves f running; Abandoned counts it until it returns. At MaxAbandoned
// outstanding calls Wedged closes and Do refuses every call with ErrWedged.
// A caller records an abandoned result as non-transient (spec §6.6).
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
	go func() { done <- f(ctx) }()
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
