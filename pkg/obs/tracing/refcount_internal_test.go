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

package tracing

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// isolateSetupState swaps the package's install state for a clean, faked one
// and restores it when the test ends.
//
// Restoring matters: the tests in tracing_test.go share the ONE real
// TracerProvider Setup installed for this binary, and each of them holds an
// unreleased reference to it. Leaving this test's counters behind would
// either retire that provider under them or leave the count permanently
// wrong.
//
// It returns a pointer to the number of times the real teardown ran.
func isolateSetupState(t *testing.T) *int {
	t.Helper()

	setupMu.Lock()
	savedRefs, savedShutdown, savedInstall := setupRefs, setupShutdown, installFn
	setupRefs, setupShutdown = 0, nil
	setupMu.Unlock()

	t.Cleanup(func() {
		setupMu.Lock()
		defer setupMu.Unlock()
		setupRefs, setupShutdown, installFn = savedRefs, savedShutdown, savedInstall
	})

	var (
		mu        sync.Mutex
		teardowns int
	)
	installFn = func(context.Context, Options) (func(context.Context) error, error) {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			teardowns++
			return nil
		}, nil
	}
	return &teardowns
}

// TestSetupIsReferenceCounted is the `clustarr all` hazard, stated exactly:
// seven services share one process and each Run defers the shutdown Setup
// handed it, so the FIRST service to return must not retire tracing for the
// six still running.
//
// Before Task C12a, Setup handed every caller the same shutdown func and a
// single sync.Once inside it, so the first deferred call flushed and closed
// the exporter for the whole process. Nothing surfaced: spans carried on
// being created and were silently dropped.
func TestSetupIsReferenceCounted(t *testing.T) {
	teardowns := isolateSetupState(t)
	ctx := context.Background()

	first, err := Setup(ctx, Options{ServiceName: "svc-a"})
	require.NoError(t, err)
	second, err := Setup(ctx, Options{ServiceName: "svc-b"})
	require.NoError(t, err)
	require.Zero(t, *teardowns, "Setup must not tear anything down")

	require.NoError(t, first(ctx))
	require.Zero(t, *teardowns,
		"the first caller's shutdown retired tracing while a second caller was still running")

	// A caller that defers its shutdown twice (or whose Run returns through
	// two paths) must release one reference, not two.
	require.NoError(t, first(ctx))
	require.Zero(t, *teardowns, "a caller's shutdown must be idempotent for that caller")

	require.NoError(t, second(ctx))
	require.Equal(t, 1, *teardowns, "the last caller's shutdown must retire tracing exactly once")

	require.NoError(t, second(ctx))
	require.Equal(t, 1, *teardowns, "the last caller's shutdown must stay idempotent")
}

// TestSetupAfterFullTeardownReinstalls proves the state is genuinely cleared
// rather than left holding a dead provider: once every reference is gone, the
// next Setup installs again. Without this, a process that stopped and
// restarted its services -- or a test binary that ran a full teardown -- would
// hand out a TracerProvider that had already been shut down.
func TestSetupAfterFullTeardownReinstalls(t *testing.T) {
	teardowns := isolateSetupState(t)
	ctx := context.Background()

	only, err := Setup(ctx, Options{ServiceName: "svc"})
	require.NoError(t, err)
	require.NoError(t, only(ctx))
	require.Equal(t, 1, *teardowns)

	again, err := Setup(ctx, Options{ServiceName: "svc"})
	require.NoError(t, err)
	require.NoError(t, again(ctx))
	require.Equal(t, 2, *teardowns, "a Setup after a full teardown must install a fresh provider")
}

// TestSetupConcurrentReferencesAreBalanced runs the acquire/release cycle
// from many goroutines: the install must happen once and the teardown must
// happen once, whatever order they interleave in. Run under -race it also
// covers the counter itself.
func TestSetupConcurrentReferencesAreBalanced(t *testing.T) {
	teardowns := isolateSetupState(t)
	ctx := context.Background()

	// One reference held across the whole fan-out, so the count cannot reach
	// zero mid-flight and reinstall.
	held, err := Setup(ctx, Options{ServiceName: "held"})
	require.NoError(t, err)

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			shutdown, err := Setup(ctx, Options{ServiceName: "concurrent"})
			if err != nil {
				return
			}
			_ = shutdown(ctx)
		}()
	}
	wg.Wait()

	require.Zero(t, *teardowns, "the held reference must keep tracing alive")
	require.NoError(t, held(ctx))
	require.Equal(t, 1, *teardowns)
}

// TestSpansStayValidAfterFullTeardown is the carried "otel provider left
// dead" item (X14): with the REAL install, the last shutdown used to leave
// the otel global pointing at the provider it had just shut down, which
// hands out no-op tracers -- so every span until the next Setup had an
// invalid context, no trace id reached a log line and Inject wrote no
// Clustarr-Trace header. A process that restarts its services, or a test
// binary that runs one after another, lost propagation in between.
func TestSpansStayValidAfterFullTeardown(t *testing.T) {
	setupMu.Lock()
	savedRefs, savedShutdown := setupRefs, setupShutdown
	setupRefs, setupShutdown = 0, nil
	setupMu.Unlock()
	t.Cleanup(func() {
		setupMu.Lock()
		defer setupMu.Unlock()
		setupRefs, setupShutdown = savedRefs, savedShutdown
	})
	ctx := context.Background()

	shutdown, err := Setup(ctx, Options{SampleRatio: 1})
	require.NoError(t, err)
	_, live := Start(ctx, "while-installed")
	require.True(t, live.SpanContext().IsValid())
	live.End()

	require.NoError(t, shutdown(ctx))
	_, after := Start(ctx, "after-teardown")
	defer after.End()
	require.True(t, after.SpanContext().IsValid(),
		"a span started after the last shutdown has an invalid context: the otel global was left on a dead provider")

	again, err := Setup(ctx, Options{SampleRatio: 1})
	require.NoError(t, err)
	_, next := Start(ctx, "after-reinstall")
	require.True(t, next.SpanContext().IsValid())
	next.End()
	require.NoError(t, again(ctx))
}
