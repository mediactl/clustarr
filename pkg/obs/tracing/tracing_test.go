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

// TestMain runs every test in this package, then exercises Setup's shutdown
// once more.
//
// pkg/obs/tracing.Setup is reference counted (see tracing.go): the provider
// is retired only when the LAST outstanding reference is released. Most tests
// in this file take a reference and never release it -- deliberately, because
// a test that retired the shared provider as part of its own cleanup would
// silently turn every span any LATER test creates invalid, which is exactly
// the failure this file hit during review round 1. Those outstanding
// references are what keep the provider alive for the whole binary, and the
// pair of calls below therefore only ever release one of them.
//
// What it still proves is that a caller's own shutdown tolerates being called
// twice: every Run in this repo defers it, and a double defer must release
// one reference, not two.
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

// TestSetupInstallsOneProviderPerProcess is the regression test for review
// round 1's finding 1: `clustarr all` runs several services concurrently in
// one process, and each calls Setup; without a guard, the second call would
// silently replace the global otel.TracerProvider (and its
// resource.service.name) out from under every span the first service's
// goroutine was about to record, and the first call's exporter would leak
// with no shutdown ever invoked on it.
//
// It asserts the property Setup actually promises -- every call leaves
// otel.GetTracerProvider() unchanged -- so it does not depend on being the
// first Setup call in this test binary (other tests in this package call
// Setup too; go test runs everything in one process per package).
//
// It deliberately says nothing about the shutdown funcs. Each caller now gets
// a closure of its own rather than the one shared func the sync.Once version
// handed out -- that is the whole point of Task C12a's reference counting --
// but two closures built at the same source line share a code pointer, so
// reflect cannot tell them apart. TestSetupIsReferenceCounted covers the
// counting directly, and TestSetupSurvivesOneCallersShutdown covers the
// behaviour that matters. This test never invokes either shutdown.
func TestSetupInstallsOneProviderPerProcess(t *testing.T) {
	shutdown1, err1 := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "svc-a", SampleRatio: 1,
	})
	require.NoError(t, err1)
	require.NotNil(t, shutdown1)
	providerAfterFirst := otel.GetTracerProvider()

	shutdown2, err2 := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "svc-b", SampleRatio: 1,
	})
	require.NoError(t, err2)
	require.NotNil(t, shutdown2)
	providerAfterSecond := otel.GetTracerProvider()

	require.True(t, providerAfterFirst == providerAfterSecond, //nolint:gocritic // comparable interface identity
		"a second Setup call with a different ServiceName must not replace the process-wide TracerProvider")
}

// TestSetupSurvivesOneCallersShutdown is the `clustarr all` scenario in
// miniature, against the real provider: two callers, the first returns early
// and runs its deferred shutdown, and the second must still be able to record
// spans afterwards.
//
// It checks the provider identity rather than span recording because the otel
// global caches its delegate tracer: after a TracerProvider is shut down, a
// tracer handle obtained BEFORE the shutdown keeps producing recording spans
// whose data is silently dropped by the stopped processor. Identity is the
// property that is actually observable from outside the SDK.
func TestSetupSurvivesOneCallersShutdown(t *testing.T) {
	first, err := tracing.Setup(context.Background(), tracing.Options{ServiceName: "svc-a", SampleRatio: 1})
	require.NoError(t, err)
	second, err := tracing.Setup(context.Background(), tracing.Options{ServiceName: "svc-b", SampleRatio: 1})
	require.NoError(t, err)
	// Keep the package's shared provider alive for every later test: this
	// test releases the first reference and holds the second for good.
	_ = second

	before := otel.GetTracerProvider()
	require.NoError(t, first(context.Background()))
	require.True(t, before == otel.GetTracerProvider(), //nolint:gocritic // comparable interface identity
		"one caller's shutdown must not retire the provider the others are still using")

	_, span := tracing.Start(context.Background(), "after-first-shutdown")
	defer span.End()
	require.True(t, span.SpanContext().IsValid(),
		"tracing must still produce valid span contexts after one of two callers has shut down")
}

// TestSetupConcurrentCallsDoNotRace is the round-1 finding's other half: run
// this with `go test -race` and it fails (or is flagged by the race
// detector) if Setup's installation of the global TracerProvider is not
// synchronized, exactly the scenario `clustarr all`'s concurrent Run calls
// create. It never calls any of the returned shutdown funcs for real.
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

	provider := otel.GetTracerProvider()
	for i := range n {
		require.NoError(t, errs[i])
		require.NotNil(t, shutdowns[i], "goroutine %d got a nil shutdown", i)
	}
	require.True(t, provider == otel.GetTracerProvider(), //nolint:gocritic // comparable interface identity
		"concurrent Setup calls must install exactly one TracerProvider")
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
