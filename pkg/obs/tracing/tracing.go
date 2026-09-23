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

// The process-wide install is reference counted rather than guarded by a
// bare sync.Once.
//
// otel.SetTracerProvider is a package-level global with no synchronization of
// its own, and Setup is called by every service's Run. `clustarr all` starts
// seven services concurrently in one process (cmd/clustarr/all.go), so
// without a guard two goroutines' calls would race on that global -- whichever
// finished last would silently become the provider every OTHER service's
// spans are then recorded against (wrong resource.service.name on most of
// them), and every TracerProvider but the last would leak with its own
// exporter connection never shut down.
//
// A sync.Once alone fixed the install but not the teardown: it handed every
// caller the SAME shutdown func, so under `clustarr all` the first service
// whose Run returned -- a --data-path validation failure, a manager that
// could not build, any early return at all -- ran that shutdown and retired
// tracing for the six services still running. The failure was silent: spans
// carried on being created and simply stopped being exported.
//
// So: the first caller installs, each caller gets a shutdown of its own, and
// the provider is retired only when the last of them has returned. A caller's
// own shutdown is idempotent (its own sync.Once), so a double defer releases
// one reference, not two. Once the count reaches zero the state is cleared,
// the otel global gets an exporter-less resting provider rather than the
// dead one (see release), and a later Setup installs a fresh provider rather
// than handing back a dead one.
//
// The first caller's Options still win for as long as any reference is held;
// callers that need a specific, honest resource.service.name when several
// services share a process (as `clustarr all` does) must agree on one Options
// value up front rather than relying on being first.
var (
	setupMu   sync.Mutex
	setupRefs int
	// setupShutdown is the real, single teardown returned by installFn. It
	// is nil exactly when setupRefs is 0.
	setupShutdown func(context.Context) error
)

// installFn is install, indirected so a test can substitute a fake that
// records how many times the real teardown ran. Production never reassigns
// it.
var installFn = install

// noopShutdown is returned alongside an error, so a caller that defers the
// shutdown it was handed without checking the error does not panic.
func noopShutdown(context.Context) error { return nil }

// Setup installs the process-wide TracerProvider that Start, Inject and
// Extract read through the otel global, and the W3C tracecontext
// propagator. It returns a shutdown func that releases this caller's
// reference and, when it is the last one, flushes buffered spans and closes
// the exporter; call it once at startup and defer the shutdown.
//
// Setup is safe to call from several goroutines and from several services in
// one process -- see the comment above [setupMu] -- and each returned
// shutdown is safe to call more than once.
func Setup(ctx context.Context, opts Options) (shutdown func(context.Context) error, err error) {
	setupMu.Lock()
	defer setupMu.Unlock()

	if setupRefs == 0 {
		sd, installErr := installFn(ctx, opts)
		if installErr != nil {
			return noopShutdown, installErr
		}
		setupShutdown = sd
	}
	setupRefs++

	var once sync.Once
	return func(ctx context.Context) error {
		var err error
		once.Do(func() { err = release(ctx) })
		return err
	}, nil
}

// release drops one reference and performs the real teardown when it was the
// last. It is the only path that clears the package state, so a Setup after a
// full teardown installs a fresh provider instead of returning a shut-down
// one.
func release(ctx context.Context) error {
	setupMu.Lock()
	defer setupMu.Unlock()

	if setupRefs == 0 {
		// Unreachable through Setup's per-caller Once; defensive so an
		// extra release can never drive the count negative and retire a
		// provider another caller still holds.
		return nil
	}
	setupRefs--
	if setupRefs > 0 {
		return nil
	}
	sd := setupShutdown
	setupShutdown = nil
	var err error
	if sd != nil {
		err = sd(ctx)
	}
	// The otel global still points at the provider just shut down, and a
	// shut-down sdktrace provider hands out no-op tracers: every span
	// started after this -- until some later Setup -- would have an invalid
	// context, so no trace id reaches a log line and Inject writes no
	// Clustarr-Trace header. In production nothing runs after the last
	// service's teardown, but a long-lived process that stops and restarts
	// its services, or a test binary that runs one after another, would
	// silently lose propagation in between. So the last release leaves a
	// resting provider in its place: it samples and so produces valid,
	// propagatable span contexts, and exports nothing -- it has no exporter
	// to flush or close, so it needs no teardown of its own.
	otel.SetTracerProvider(restingProvider())
	return err
}

// restingProvider is the TracerProvider the otel global holds between a full
// teardown and the next Setup; see release.
func restingProvider() trace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
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
