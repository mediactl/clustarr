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
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// TestInjectExtractCarriesTheTraceAcrossAnEnvelope is the single most
// valuable test in this package: it proves a trace survives the hop between
// two services over NATS, via events.Envelope.Trace / events.HeaderTrace.
func TestInjectExtractCarriesTheTraceAcrossAnEnvelope(t *testing.T) {
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	ctx, span := tracing.Start(context.Background(), "producer")
	want := span.SpanContext().TraceID()
	e := &events.Envelope{Type: "test"}
	tracing.Inject(ctx, e)
	span.End()

	require.NotEmpty(t, e.Trace, "Clustarr-Trace must be populated")

	got := tracing.Extract(context.Background(), e)
	_, consumer := tracing.Start(got, "consumer")
	defer consumer.End()
	require.Equal(t, want, consumer.SpanContext().TraceID(),
		"consumer span must join the producer trace")
}

func TestInjectWithNoActiveSpanLeavesTraceEmpty(t *testing.T) {
	e := &events.Envelope{Type: "test"}
	tracing.Inject(context.Background(), e)
	require.Empty(t, e.Trace, "Inject must not fabricate a trace when ctx carries no span")
}

func TestExtractWithNoTraceHeaderReturnsContextUnchanged(t *testing.T) {
	e := &events.Envelope{Type: "test"}
	ctx := context.Background()

	got := tracing.Extract(ctx, e)

	require.Equal(t, ctx, got, "Extract must be a no-op when the envelope carries no trace header")
}
