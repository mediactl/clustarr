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

package k8s_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// startJetStream boots an embedded JetStream server on loopback, with its
// store under the test's temporary directory. No external broker and no
// network: it is the same helper pkg/events/natsbus's own suite uses.
func startJetStream(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)

	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-bushooks",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// TestConnectBusCarriesTheTraceWhenBuiltTheWayAServiceBuildsIt is Task C12a
// step 2b's proof.
//
// Task C1 put the publish and receive hooks inside pkg/events so no bus
// implementation could forget them, and pkg/obs.BusHooks() supplies the pair
// that satisfies them -- but the ONE real construction site,
// [k8s.ConnectBus], passed no options at all. The propagation existed and
// never ran in production, so amendment §A4's "first end-to-end trace at M1"
// could not have been true.
//
// This test builds the bus exactly as catalogarr, importarr, indexarr,
// grabarr, squasharr and captionarr now build it -- obs.Bootstrap's
// TracerProvider installed first, then ConnectBus with
// k8s.WithBusHooks(obs.BusHooks()) -- publishes inside a span, and asserts the
// trace survives the wire: the delivered envelope carries a traceparent for
// the publishing trace, and the handler's own context is a child of it.
//
// cmd/clustarr's TestEveryServicePassesBusHooks is the other half: this test
// proves the mechanism works, that one proves every service actually calls
// it.
func TestConnectBusCarriesTheTraceWhenBuiltTheWayAServiceBuildsIt(t *testing.T) {
	url := startJetStream(t)

	// A service's Run calls obs.Bootstrap (which calls tracing.Setup) before
	// it calls ConnectBus. Order matters: Inject and Extract read and write
	// through the otel globals Setup installs, so hooks wired before it are
	// a silent no-op.
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "catalogarr", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("tracing shutdown: %v", err)
		}
	})

	bus, nc, err := k8s.ConnectBus(url, "catalogarr", k8s.WithBusHooks(obs.BusHooks()))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	t.Cleanup(func() {
		if err := bus.Close(); err != nil {
			t.Errorf("bus close: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), contracttest.Timeout)
	t.Cleanup(cancel)
	require.NoError(t, k8s.EnsureTopology(ctx, bus, contracttest.Topology()))

	type delivery struct {
		envTrace string
		ctxSpan  trace.SpanContext
	}
	got := make(chan delivery, 1)
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamEvents,
		Durable:     "bushooks",
		Filters:     []string{events.FilterAllEvents},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 4,
	}, func(ctx context.Context, m events.Message) error {
		select {
		case got <- delivery{
			envTrace: m.Envelope().Trace,
			ctxSpan:  trace.SpanContextFromContext(ctx),
		}:
		default:
		}
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	publishCtx, span := tracing.Start(ctx, "bushooks.publish")
	publisher := span.SpanContext()
	require.True(t, publisher.IsValid(), "tracing.Setup must produce valid span contexts")

	data, err := json.Marshal(map[string]string{"k": "v"})
	require.NoError(t, err)
	_, err = bus.Publish(publishCtx, events.CatalogItemSubject("movie", events.ActionAdded, "uid-bushooks"),
		&events.Envelope{
			ID:     "evt-bushooks",
			Type:   "catalogarr",
			Schema: "catalog.ItemEvent.v1",
			Source: "catalogarr@0.0.0",
			Key:    "default/bushooks",
			// Left empty on purpose: the hook, not the caller, must be what
			// stamps it. A caller that filled it in would prove nothing.
			Trace: "",
			Data:  data,
		})
	require.NoError(t, err)
	span.End()

	select {
	case d := <-got:
		require.NotEmpty(t, d.envTrace,
			"the delivered envelope carried no traceparent: obs.BusHooks().BeforePublish never ran, "+
				"which is exactly the production gap ConnectBus's options close")
		require.Contains(t, d.envTrace, publisher.TraceID().String(),
			"the envelope's traceparent does not name the publishing trace")
		require.True(t, d.ctxSpan.IsValid(),
			"the handler's context carried no span context: obs.BusHooks().AfterReceive never ran")
		require.Equal(t, publisher.TraceID(), d.ctxSpan.TraceID(),
			"the handler's context is not part of the publisher's trace")
	case <-time.After(contracttest.Timeout):
		t.Fatal("the handler was never invoked")
	}
}

// TestConnectBusWithoutHooksCarriesNoTrace is the control: the same publish
// through a bus built with no options leaves the envelope's traceparent
// empty. Without it, the test above could pass on an envelope that was
// stamped somewhere else entirely.
func TestConnectBusWithoutHooksCarriesNoTrace(t *testing.T) {
	url := startJetStream(t)

	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		ServiceName: "catalogarr", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("tracing shutdown: %v", err)
		}
	})

	bus, nc, err := k8s.ConnectBus(url, "catalogarr")
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	t.Cleanup(func() {
		if err := bus.Close(); err != nil {
			t.Errorf("bus close: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), contracttest.Timeout)
	t.Cleanup(cancel)
	require.NoError(t, k8s.EnsureTopology(ctx, bus, contracttest.Topology()))

	got := make(chan string, 1)
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamEvents,
		Durable:     "bushooks-off",
		Filters:     []string{events.FilterAllEvents},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 4,
	}, func(_ context.Context, m events.Message) error {
		select {
		case got <- m.Envelope().Trace:
		default:
		}
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	publishCtx, span := tracing.Start(ctx, "bushooks.publish")
	data, err := json.Marshal(map[string]string{"k": "v"})
	require.NoError(t, err)
	_, err = bus.Publish(publishCtx, events.CatalogItemSubject("movie", events.ActionAdded, "uid-bushooks-off"),
		&events.Envelope{
			ID:     "evt-bushooks-off",
			Type:   "catalogarr",
			Schema: "catalog.ItemEvent.v1",
			Source: "catalogarr@0.0.0",
			Key:    "default/bushooks",
			Data:   data,
		})
	require.NoError(t, err)
	span.End()

	select {
	case trace := <-got:
		require.Empty(t, trace, "a bus built with no options must not stamp a traceparent")
	case <-time.After(contracttest.Timeout):
		t.Fatal("the handler was never invoked")
	}
}
