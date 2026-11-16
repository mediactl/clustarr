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
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seams sets the grace and the wedge threshold for one test and resets the
// process-wide abandonment state around it. No test in this package is
// parallel: the state is the process's.
func seams(t *testing.T, g time.Duration, max int) {
	t.Helper()
	resetDo()
	oldGrace, oldMax := grace, maxAbandoned
	grace, maxAbandoned = g, max
	t.Cleanup(func() { grace, maxAbandoned = oldGrace, oldMax; resetDo() })
}

func TestDoReturnsTheBodysResult(t *testing.T) {
	seams(t, 50*time.Millisecond, MaxAbandoned)
	boom := errors.New("boom")
	assert.NoError(t, Do(context.Background(), func(context.Context) error { return nil }))
	assert.ErrorIs(t, Do(context.Background(), func(context.Context) error { return boom }), boom)
}

func TestDoNeverStartsAfterItsContextEnded(t *testing.T) {
	seams(t, 50*time.Millisecond, MaxAbandoned)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := Do(ctx, func(context.Context) error { ran = true; return nil })
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, ran)
}

// A body that honours its context inside the grace is not abandoned.
func TestABodyThatStopsInsideTheGraceIsNotAbandoned(t *testing.T) {
	seams(t, time.Second, MaxAbandoned)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := Do(ctx, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrAbandoned)
	assert.Zero(t, Abandoned())
}

// The old process-group kill's replacement: a call blocked past its context
// returns after the grace, and is counted until it returns (spec §7.4).
func TestACancelledCallReturnsWithinTheGraceAndIsCountedUntilItReturns(t *testing.T) {
	seams(t, 100*time.Millisecond, MaxAbandoned)
	release := make(chan struct{})
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Do(ctx, func(context.Context) error { <-release; return nil })
	assert.Less(t, time.Since(start), time.Second)
	assert.ErrorIs(t, err, ErrAbandoned)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, Abandoned())
	close(release)
	assert.Eventually(t, func() bool { return Abandoned() == 0 }, time.Second, 5*time.Millisecond,
		"a call that returns after being abandoned is no longer counted")
}

func TestMaxAbandonedWedgesTheProcess(t *testing.T) {
	seams(t, 10*time.Millisecond, 2)
	release := make(chan struct{})
	defer close(release)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		require.ErrorIs(t, Do(ctx, func(context.Context) error { <-release; return nil }), ErrAbandoned)
		cancel()
	}
	select {
	case <-Wedged():
	case <-time.After(time.Second):
		t.Fatal("Wedged() did not close at MaxAbandoned outstanding calls")
	}
	assert.ErrorIs(t, Do(context.Background(), func(context.Context) error { return nil }), ErrWedged)
	assert.ErrorIs(t, Healthz(nil), ErrWedged)
}

// sliceTo panics as ffgo's goString did on 2026-11-11: a slice past its
// array's length, a runtime.Error.
func sliceTo(n int) error {
	var buf [4096]byte
	_ = buf[:n]
	return nil
}

// A panic in the call is an error, not the process's end: Do's goroutine is
// out of reach of the bus's handler recover, so an unrecovered one in a
// binding killed the import agent at every delivery of a scan.
func TestAPanickingCallIsAnErrorNamingWhereItPanicked(t *testing.T) {
	seams(t, 50*time.Millisecond, MaxAbandoned)
	before := Panics()
	closed := false
	err := Do(context.Background(), func(context.Context) error {
		defer func() { closed = true }()
		return sliceTo(4097)
	})
	require.ErrorIs(t, err, ErrPanicked)
	var rerr runtime.Error
	require.ErrorAs(t, err, &rerr, "the panic's own runtime.Error is wrapped")
	var perr *PanicError
	require.ErrorAs(t, err, &perr)
	assert.Contains(t, perr.Frame, "ffruntime.sliceTo (do_test.go:", "the frame is the panicking function, not the runtime's")
	assert.Contains(t, err.Error(), "slice bounds out of range [:4097] with length 4096")
	assert.Contains(t, err.Error(), "ffruntime.sliceTo")
	assert.Contains(t, string(perr.Stack), "ffruntime.sliceTo")
	assert.True(t, closed, "the call's own defers ran before the recover")
	assert.Equal(t, before+1, Panics())
	assert.NotErrorIs(t, err, ErrAbandoned)
	assert.NoError(t, Do(context.Background(), func(context.Context) error { return nil }), "the next call runs")
}

func TestAPanicWithAValueThatIsNoErrorIsAnError(t *testing.T) {
	seams(t, 50*time.Millisecond, MaxAbandoned)
	err := Do(context.Background(), func(context.Context) error { panic("boom") })
	require.ErrorIs(t, err, ErrPanicked)
	var rerr runtime.Error
	assert.False(t, errors.As(err, &rerr))
	assert.Contains(t, err.Error(), "boom (at github.com/mediactl/clustarr/pkg/ffruntime.TestAPanicWithAValueThatIsNoErrorIsAnError.func1")
}
