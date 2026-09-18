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

package tracing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// TestMain runs every test in this package, then -- and only then -- retires
// the single process-wide TracerProvider pkg/obs/tracing.Setup's once-guard
// installs (see tracing.go and TestSetupIsOncePerProcess below). Every test
// in this package shares that one provider; a test that shut it down as part
// of its own cleanup would silently turn every span any LATER test creates
// invalid (a real TracerProvider stops recording once Shutdown has run),
// which is exactly the failure this file hit during review round 1's fix --
// the pre-existing tests each called t.Cleanup(shutdown) under the old,
// per-call semantics, and the first one to run killed the shared provider
// for the rest of the suite. Doing the real shutdown-and-verify-idempotent
// check here, after m.Run(), is what lets every other test safely assume the
// provider it gets from Setup is live for the whole test binary.
func TestMain(m *testing.M) {
	code := m.Run()

	shutdown, err := tracing.Setup(context.Background(), tracing.Options{SampleRatio: 1})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracing_test: TestMain: Setup: %v\n", err)
		os.Exit(1)
	}
	if err := shutdown(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "tracing_test: TestMain: shutdown: %v\n", err)
		os.Exit(1)
	}
	// The returned shutdown must tolerate a second call -- every caller of
	// Setup gets the same func back and every one of them may defer it.
	if err := shutdown(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "tracing_test: TestMain: shutdown was not idempotent: %v\n", err)
		os.Exit(1)
	}

	os.Exit(code)
}

func TestSetupDisabledStillProducesValidSpanContexts(t *testing.T) {
	// No t.Cleanup(shutdown) here: Setup is once-per-process and every test
	// in this package shares the provider it installs; see TestMain.
	_, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)

	_, span := tracing.Start(context.Background(), "op")
	defer span.End()

	require.True(t, span.SpanContext().IsValid(),
		"Setup with Enabled=false must still install a TracerProvider that produces valid span contexts")
}

func TestSetupDefaultsServiceNameWhenEmpty(t *testing.T) {
	_, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, SampleRatio: 1,
	})
	require.NoError(t, err)
}

// TestRecordErrorSetsSpanStatus uses a tracetest.SpanRecorder, bypassing the
// otel global entirely, so it asserts against the actual recorded status and
// events rather than merely confirming RecordError does not panic.
func TestRecordErrorSetsSpanStatus(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	tracing.RecordError(span, errors.New("boom"))
	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Error, ended[0].Status().Code)
	require.Equal(t, "boom", ended[0].Status().Description)

	events := ended[0].Events()
	require.Len(t, events, 1)
	require.Equal(t, "exception", events[0].Name)
}

// TestStartEnrichesTheLoggerCarriedByContext proves the enrich wiring: a
// logger fetched from the context Start returns carries the span's
// trace_id and span_id, so every log line emitted under the span is
// correlated with it in any log backend.
func TestStartEnrichesTheLoggerCarriedByContext(t *testing.T) {
	_, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := logging.NewContext(context.Background(), base)

	ctx, span := tracing.Start(ctx, "op")
	defer span.End()

	logging.FromContext(ctx).Info("hello")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))

	sc := span.SpanContext()
	require.Equal(t, sc.TraceID().String(), record["trace_id"])
	require.Equal(t, sc.SpanID().String(), record["span_id"])
}

// TestSetupIsOncePerProcess is the regression test for review round 1's
// finding 1: `clustarr all` runs several services concurrently in one
// process, and each calls Setup; without a once-guard, the second call would
// silently replace the global otel.TracerProvider (and its
// resource.service.name) out from under every span the first service's
// goroutine was about to record, and the first call's exporter would leak
// with no shutdown ever invoked on it.
//
// It asserts the property Setup actually promises -- every call, not just
// "the first vs the second", returns the identical shutdown func and leaves
// otel.GetTracerProvider() unchanged -- so it does not depend on being the
// first Setup call in this test binary (other tests in this package call
// Setup too; go test runs everything in one process per package). It never
// invokes either shutdown for real: that would retire the provider every
// other test in this package shares -- see TestMain, which is the one place
// allowed to do that.
func TestSetupIsOncePerProcess(t *testing.T) {
	shutdown1, err1 := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "svc-a", SampleRatio: 1,
	})
	require.NoError(t, err1)
	providerAfterFirst := otel.GetTracerProvider()

	shutdown2, err2 := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "svc-b", SampleRatio: 1,
	})
	require.NoError(t, err2)
	providerAfterSecond := otel.GetTracerProvider()

	require.True(t, providerAfterFirst == providerAfterSecond, //nolint:gocritic // comparable interface identity
		"a second Setup call with a different ServiceName must not replace the process-wide TracerProvider")
	require.Equal(t,
		reflect.ValueOf(shutdown1).Pointer(), reflect.ValueOf(shutdown2).Pointer(),
		"every Setup call must return the same shutdown func")
}

// TestSetupConcurrentCallsDoNotRace is the round-1 finding's other half: run
// this with `go test -race` and it fails (or is flagged by the race
// detector) if Setup's installation of the global TracerProvider is not
// synchronized, exactly the scenario `clustarr all`'s concurrent Run calls
// create. Like TestSetupIsOncePerProcess, it never calls any of the returned
// shutdown funcs for real.
func TestSetupConcurrentCallsDoNotRace(t *testing.T) {
	const n = 16
	shutdowns := make([]func(context.Context) error, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			shutdowns[i], errs[i] = tracing.Setup(context.Background(), tracing.Options{
				ServiceName: "concurrent", SampleRatio: 1,
			})
		}()
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		require.Equal(t,
			reflect.ValueOf(shutdowns[0]).Pointer(), reflect.ValueOf(shutdowns[i]).Pointer(),
			"goroutine %d got a different shutdown func than goroutine 0", i)
	}
}

func TestRecordErrorWithNilErrorIsANoOp(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	tracing.RecordError(span, nil)
	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Unset, ended[0].Status().Code)
	require.Empty(t, ended[0].Events())
}
