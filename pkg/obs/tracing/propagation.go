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

	"go.opentelemetry.io/otel/propagation"

	"github.com/mediactl/clustarr/pkg/events"
)

// traceparentKey is the W3C Trace Context carrier key that
// propagation.TraceContext reads and writes. It is an implementation detail
// of the propagator, distinct from events.HeaderTrace, the wire header name
// its value is copied to and from.
const traceparentKey = "traceparent"

// Inject writes the span context carried by ctx into e.Trace (wire header
// events.HeaderTrace) as a W3C traceparent, so the next service to receive e
// over NATS can continue the same trace. It is a no-op, leaving e.Trace
// untouched, if ctx carries no valid span context (for example, tracing was
// never started, or Setup was never called).
func Inject(ctx context.Context, e *events.Envelope) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	if tp := carrier.Get(traceparentKey); tp != "" {
		e.Trace = tp
	}
}

// Extract reverses Inject: it returns a context carrying the span context
// encoded in e.Trace, so a span Start-ed from the result continues the
// producer's trace rather than beginning a new one. If e is nil or carries
// no trace header, Extract returns ctx unchanged.
func Extract(ctx context.Context, e *events.Envelope) context.Context {
	if e == nil || e.Trace == "" {
		return ctx
	}

	carrier := propagation.MapCarrier{traceparentKey: e.Trace}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}
