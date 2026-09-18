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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

func TestSetupDisabledStillProducesValidSpanContexts(t *testing.T) {
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	_, span := tracing.Start(context.Background(), "op")
	defer span.End()

	require.True(t, span.SpanContext().IsValid(),
		"Setup with Enabled=false must still install a TracerProvider that produces valid span contexts")
}

func TestSetupDefaultsServiceNameWhenEmpty(t *testing.T) {
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })
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
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

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
