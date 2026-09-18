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

package obs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// syncBuffer is an io.Writer a logger can be pointed at from several
// goroutines. slog.Handler does not serialize writes for us.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBootstrapBridgesControllerRuntimeToSlog is the assertion whose absence
// let the bridge ship as dead code.
//
// cmd/clustarr's PersistentPreRunE used to call ctrl.SetLogger(zap.New(...))
// before any RunE, and controller-runtime's delegating sink honours only the
// FIRST SetLogger per process. Every service's own
// ctrl.SetLogger(logging.LogrBridge(...)) was therefore a silent no-op, and
// one process emitted two different JSON schemas: slog's {time,level,msg}
// from the services and zap's {ts,level,logger,msg} from controller-runtime.
// Nothing failed, so six reviews missed it.
//
// The test asserts what an operator would actually have seen: a line written
// through ctrl.Log -- controller-runtime's own logger, the one
// ctrl.LoggerFrom(ctx) falls back to -- has to land in the slog handler
// Bootstrap built, with slog's keys and not zap's.
func TestBootstrapBridgesControllerRuntimeToSlog(t *testing.T) {
	var out syncBuffer

	// The returned shutdown is deliberately NOT deferred here. tracing.Setup
	// installs ONE TracerProvider for the whole test binary (this package's
	// tests all run in the same process) and hands every Bootstrap caller
	// back the exact same shutdown closure -- see Setup's doc comment.
	// Calling it would flip that shared provider's isShutdown flag, and
	// go.opentelemetry.io/otel/sdk/trace.TracerProvider.Tracer returns a
	// no-op tracer once that flag is set, permanently, for the rest of the
	// process: every span any later test in this package starts -- notably
	// TestBusHooksCarryOneTraceAcrossPublishAndConsume, this package's only
	// real exercise of tracing.Inject/Extract -- would silently come back
	// invalid. There is nothing this call would actually flush anyway:
	// Enabled is false here, so Setup never registers an exporter batcher.
	ctx, _, err := Bootstrap(context.Background(),
		logging.Options{Output: &out, Level: slog.LevelDebug},
		tracing.Options{ServiceName: "bootstrap-test"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if logging.FromContext(ctx) == nil {
		t.Fatal("Bootstrap returned a context with no logger")
	}

	ctrl.Log.Info("probe", "k", "v")

	line := out.String()
	if line == "" {
		t.Fatalf("ctrl.Log.Info wrote nothing to Bootstrap's logger: " +
			"controller-runtime is still logging somewhere else")
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(line), "\n", 2)[0]), &rec); err != nil {
		t.Fatalf("ctrl.Log output is not the JSON slog emits: %v\n%s", err, line)
	}

	// slog's JSONHandler keys. zap's are "ts" and "logger"; if those show up
	// the bridge was not installed and controller-runtime kept its own
	// backend.
	for _, key := range []string{"time", "level", "msg"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("ctrl.Log record has no %q key, want slog's schema: %v", key, rec)
		}
	}
	for _, key := range []string{"ts", "logger", "stacktrace"} {
		if _, ok := rec[key]; ok {
			t.Errorf("ctrl.Log record has zap's %q key: controller-runtime is not on the slog bridge: %v",
				key, rec)
		}
	}
	if rec["msg"] != "probe" {
		t.Errorf("msg = %v, want \"probe\"", rec["msg"])
	}
	if rec["k"] != "v" {
		t.Errorf("k = %v, want \"v\": the bridge dropped the key/value args", rec["k"])
	}

	// Part two, in the same test function because ctrl.SetLogger is
	// genuinely single-shot per PROCESS: only the first Bootstrap in this
	// test binary can bind controller-runtime at all, so a second test
	// function could not observe the binding no matter what it did.
	//
	// This pins why Bootstrap holds a sync.Once instead of calling
	// ctrl.SetLogger directly: `clustarr all` calls Bootstrap seven times in
	// one process, and a later caller's logger legitimately does not win.
	// What must not happen is the code pretending otherwise.
	t.Run("a second Bootstrap does not rebind controller-runtime", func(st *testing.T) {
		var second syncBuffer
		// Same reasoning as the outer call: shutdown2 is the identical
		// shared closure the outer Bootstrap call already got back, so
		// discard it here too rather than shut down the process-wide
		// TracerProvider other tests in this package still need.
		ctx2, _, err := Bootstrap(context.Background(),
			logging.Options{Output: &second}, tracing.Options{})
		if err != nil {
			st.Fatalf("second Bootstrap: %v", err)
		}

		before := len(out.String())
		ctrl.Log.Info("after the second bootstrap")

		if second.String() != "" {
			st.Errorf("the second Bootstrap rebound controller-runtime's logger; it honours "+
				"only the first SetLogger per process: %s", second.String())
		}
		if !strings.Contains(out.String()[before:], "after the second bootstrap") {
			st.Errorf("controller-runtime's logger is no longer the first Bootstrap's: %s",
				out.String()[before:])
		}

		// Each caller still gets its OWN logger on its OWN context, which
		// is what every service actually logs through; only the
		// controller-runtime binding is process-wide.
		logging.FromContext(ctx2).Info("service line")
		if !strings.Contains(second.String(), "service line") {
			st.Errorf("the second Bootstrap's context does not carry its own logger: %s", second.String())
		}
	})
}

// TestBusHooksCarryOneTraceAcrossPublishAndConsume is Task C1's proof that a
// trace actually crosses the bus, not just that the hook plumbing compiles.
// It is the first real exercise of tracing.Inject and tracing.Extract, which
// until this task had no production call sites (see
// docs/observability.md's Status banner before this change).
//
// It wires BusHooks() -- exactly what a service's Run is expected to pass to
// its bus constructor after calling Bootstrap -- into an in-memory bus,
// starts a span standing in for the publisher, and asserts two things about
// what the consumer's handler observes:
//
//   - the span context AfterReceive (tracing.Extract) placed on the
//     handler's ctx is the publisher's own span context, marked remote --
//     the parent-child link an exporter would otherwise be needed to see;
//   - a span started from that context shares the publisher's trace id but
//     is a genuinely distinct span.
func TestBusHooksCarryOneTraceAcrossPublishAndConsume(t *testing.T) {
	// The returned shutdown is deliberately not deferred -- see the comment
	// on the first Bootstrap call in TestBootstrapBridgesControllerRuntimeToSlog:
	// it is one closure shared by every Bootstrap call in this process, and
	// calling it would permanently disable this package's shared
	// TracerProvider for any test that runs after this one.
	ctx, _, err := Bootstrap(context.Background(),
		logging.Options{Output: io.Discard},
		tracing.Options{ServiceName: "bushooks-test"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	bus := membus.New(nil, membus.WithHooks(BusHooks()))
	t.Cleanup(func() { _ = bus.Close() })

	top := events.Default().ForSingleNode()
	top.Consumers = nil
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	type observed struct {
		// parentFromCtx is what AfterReceive (tracing.Extract) put on the
		// handler's context: the exact span context a child span would
		// attach to.
		parentFromCtx trace.SpanContext
		// childSpan is a real span started from that context.
		childSpan trace.SpanContext
	}
	got := make(chan observed, 1)

	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamEvents,
		Durable:     "bushooks-consumer",
		Filters:     []string{events.FilterAllEvents},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 1,
	}, func(ctx context.Context, _ events.Message) error {
		parent := trace.SpanContextFromContext(ctx)
		_, span := tracing.Start(ctx, "consume")
		defer span.End()
		select {
		case got <- observed{parentFromCtx: parent, childSpan: span.SpanContext()}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)

	pubCtx, pubSpan := tracing.Start(ctx, "publish")
	publisher := pubSpan.SpanContext()
	subject := events.CatalogItemSubject("movie", events.ActionAdded, "bushooks-uid")
	_, err = bus.Publish(pubCtx, subject, &events.Envelope{
		ID:     "bushooks-evt",
		Type:   "t",
		Schema: "s",
		Source: "bushooks@0.0.0",
		Key:    "default/x",
		Data:   []byte(`{}`),
	})
	pubSpan.End()
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case o := <-got:
		if !o.parentFromCtx.IsValid() {
			t.Fatal("AfterReceive did not put a valid span context on the handler's context: " +
				"the trace did not cross the bus")
		}
		if !o.parentFromCtx.IsRemote() {
			t.Error("the extracted span context is not marked remote: it did not come from propagation")
		}
		if o.parentFromCtx.TraceID() != publisher.TraceID() {
			t.Errorf("extracted trace id = %s, want the publisher's %s",
				o.parentFromCtx.TraceID(), publisher.TraceID())
		}
		if o.parentFromCtx.SpanID() != publisher.SpanID() {
			t.Errorf("extracted parent span id = %s, want the publisher's own span id %s "+
				"(this is the parent-child link)", o.parentFromCtx.SpanID(), publisher.SpanID())
		}
		if o.childSpan.TraceID() != publisher.TraceID() {
			t.Errorf("consumer's own span trace id = %s, want the same trace %s",
				o.childSpan.TraceID(), publisher.TraceID())
		}
		if o.childSpan.SpanID() == publisher.SpanID() {
			t.Error("consumer's own span id equals the publisher's: it is not a distinct child span")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler was never invoked: the trace never crossed the bus")
	}
}
