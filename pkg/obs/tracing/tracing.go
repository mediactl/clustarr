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

// Package tracing provides the OpenTelemetry spans shared by every Clustarr
// service: process-wide setup, span creation enriched onto the logger
// carried by context, error recording, and propagation of the active span
// across the NATS envelope's Clustarr-Trace header (see
// github.com/mediactl/clustarr/pkg/events, HeaderTrace and Envelope.Trace),
// so one trace can span the whole pipeline: search, grab, download, import,
// transcode and subtitle fetch.
package tracing

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// tracerName is the instrumentation scope every Clustarr span is recorded
// under.
const tracerName = "clustarr"

// defaultServiceName is used when Options.ServiceName is empty, so Setup
// never registers a resource with a blank service.name.
const defaultServiceName = "clustarr"

// Options configures Setup.
type Options struct {
	// Enabled turns on the OTLP gRPC exporter. When false, Setup still
	// installs a real TracerProvider — sampling per SampleRatio but
	// exporting nowhere — so Start always returns spans with valid,
	// propagatable contexts and callers need no collector to test against
	// this package.
	Enabled bool

	// Endpoint is the OTLP gRPC collector endpoint, e.g.
	// "otel-collector:4317". Read only when Enabled is true.
	Endpoint string

	// Insecure disables transport security on the OTLP gRPC connection.
	// Read only when Enabled is true.
	Insecure bool

	// ServiceName is recorded as the resource's service.name attribute.
	ServiceName string

	// SampleRatio is the fraction (0..1) of root spans sampled, applied
	// through a parent-based sampler: a span whose parent was sampled is
	// always sampled itself, regardless of ratio.
	SampleRatio float64
}

// enrich attaches span identifiers to the logger carried by ctx. It is a
// package variable, rather than a direct call, so Start stays testable in
// isolation; it points at pkg/obs/logging.With, which had the same
// signature and existed by the time this package was implemented.
var enrich = logging.With

// setupOnce guards the process-wide install: otel.SetTracerProvider is a
// package-level global with no synchronization of its own, and Setup is
// called by every service's Run. `clustarr all` starts several services
// concurrently in one process (cmd/clustarr/all.go), so without this two
// goroutines' calls would race on that global -- whichever finished last
// would silently become the provider every OTHER service's spans are then
// recorded against (wrong resource.service.name on most of them), and every
// TracerProvider but the last would leak with its own exporter connection
// never shut down.
//
// The first caller's Options therefore win for the life of the process;
// every later caller -- concurrent or not -- gets back the exact same
// shutdown func and a nil error unless the first call itself failed. Callers
// that need a specific, honest resource.service.name when several services
// might share a process (as `clustarr all` does) must agree on one Options
// value up front rather than relying on being first.
var (
	setupOnce     sync.Once
	setupShutdown func(context.Context) error
	setupErr      error
)

// Setup installs the process-wide TracerProvider that Start, Inject and
// Extract read through the otel global, and the W3C tracecontext
// propagator. It returns a shutdown func that flushes buffered spans and
// releases the exporter; call it once at startup and defer the shutdown.
//
// Setup itself is safe to call more than once per process -- see
// [setupOnce] -- and the returned shutdown is safe to call more than once
// too, in case two callers each defer the same shutdown they were handed.
func Setup(ctx context.Context, opts Options) (shutdown func(context.Context) error, err error) {
	setupOnce.Do(func() {
		setupShutdown, setupErr = install(ctx, opts)
	})
	return setupShutdown, setupErr
}

// install does the one-time work Setup performs exactly once per process.
func install(ctx context.Context, opts Options) (func(context.Context) error, error) {
	name := opts.ServiceName
	if name == "" {
		name = defaultServiceName
	}
	res := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(name))

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))),
	}

	if opts.Enabled {
		clientOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(opts.Endpoint)}
		if opts.Insecure {
			clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
		}
		exp, expErr := otlptracegrpc.New(ctx, clientOpts...)
		if expErr != nil {
			return nil, fmt.Errorf("tracing: dial OTLP exporter: %w", expErr)
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var (
		shutdownOnce sync.Once
		shutdownErr  error
	)
	shutdown := func(ctx context.Context) error {
		shutdownOnce.Do(func() { shutdownErr = tp.Shutdown(ctx) })
		return shutdownErr
	}
	return shutdown, nil
}

// Start begins a span named name under the "clustarr" instrumentation
// scope and returns a context carrying both the span and a logger enriched
// with its trace_id and span_id (via enrich; see its doc comment), so every
// log line emitted under the span can be correlated with it.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name, opts...)

	if sc := span.SpanContext(); sc.IsValid() {
		ctx = enrich(ctx,
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	}

	return ctx, span
}

// RecordError records err as an exception event on span and marks the
// span's status as an error, so a failed operation is findable by status in
// any trace backend even when its handler forgot to log. It is a no-op when
// err is nil.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
