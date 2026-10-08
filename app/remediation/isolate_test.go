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

package remediation

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
)

func TestIsolateClassifiesOutcomes(t *testing.T) {
	r := &Reconciler{}
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		fn     func(context.Context) error
		reason string
	}{
		{"ok", func(context.Context) error { return nil }, ""},
		{"transient", func(context.Context) error { return Transient(errors.New("nats: no responders")) }, failTransient},
		{"deadline", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, failTransient},
		{"EIO", func(context.Context) error { return &fsPathError{syscall.EIO} }, failTransient},
		{"bus closed", func(context.Context) error { return events.ErrClosed }, failTransient},
		{"cas miss", func(context.Context) error { return records.ErrRaced }, failCASMiss},
		{"key exists", func(context.Context) error { return events.ErrKeyExists }, failCASMiss},
		{"error", func(context.Context) error { return errors.New("no such RootFolder") }, failError},
		{"panic", func(context.Context) error { panic("boom") }, failPanic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := r.isolate(ctx, PlannerNaming, "gather", 20*time.Millisecond, tc.fn)
			assert.Equal(t, tc.reason, o.reason)
			if tc.reason == failPanic {
				assert.Equal(t, "gather panicked: boom", o.message)
			}
		})
	}
}

// TestEachIsolatedPhaseIsAChildSpan is loop spec §3.19: under
// remediation.Reconcile every planner phase is its own span,
// remediation.<planner>.<phase>, and a failed one carries an error status.
// It swaps the global tracer provider, so no test in this package may run
// in parallel with it.
func TestEachIsolatedPhaseIsAChildSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r := &Reconciler{}
	ctx, parent := tracing.Start(t.Context(), "remediation.Reconcile")
	r.isolate(ctx, PlannerNaming, "gather", 0, func(context.Context) error { return nil })
	r.isolate(ctx, PlannerNaming, "plan", 0, func(context.Context) error { return errors.New("no such RootFolder") })
	r.isolate(ctx, PlannerMarkers, "effect", 0, func(context.Context) error { panic("boom") })
	parent.End()

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = s
	}
	for _, n := range []string{"remediation.naming.gather", "remediation.naming.plan", "remediation.markers.effect"} {
		require.Contains(t, byName, n)
		assert.Equal(t, parent.SpanContext().SpanID(), byName[n].Parent().SpanID(), "%s is a child of remediation.Reconcile", n)
	}
	assert.Equal(t, codes.Unset, byName["remediation.naming.gather"].Status().Code)
	assert.Equal(t, codes.Error, byName["remediation.naming.plan"].Status().Code)
	assert.Equal(t, codes.Error, byName["remediation.markers.effect"].Status().Code)
}

type fsPathError struct{ err error }

func (e *fsPathError) Error() string { return "stat /data/x: " + e.err.Error() }
func (e *fsPathError) Unwrap() error { return e.err }
