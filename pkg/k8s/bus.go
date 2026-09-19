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

package k8s

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/version"
)

// Bus connection settings shared by every service.
const (
	// BusConnectTimeout bounds the initial dial. A service that cannot reach
	// JetStream at startup still starts: it comes up unready and keeps
	// reconnecting, which is better than crash-looping a controller that also
	// has Kubernetes work to do.
	BusConnectTimeout = 10 * time.Second

	// BusReconnectWait is the pause between reconnect attempts.
	BusReconnectWait = 2 * time.Second
)

// BusOption configures the JetStream bus [ConnectBus] builds. It is
// natsbus.Option under another name so a service's Run does not have to
// import the concrete bus package just to pass an option through.
type BusOption = natsbus.Option

// WithBusHooks installs the observability hooks h on the bus.
//
// This package deliberately does NOT default them to pkg/obs.BusHooks(), and
// deliberately does not import pkg/obs at all. Two reasons, in order:
//
//  1. Ordering. tracing.Inject and tracing.Extract read and write through the
//     otel GLOBAL propagator and TracerProvider, which obs.Bootstrap installs.
//     Hooks wired before Bootstrap has run are a silent no-op -- a nil
//     propagator injects nothing. Run already calls Bootstrap and then
//     ConnectBus, in that order, in one visible sequence; a default installed
//     inside ConnectBus would put a package that knows nothing about
//     Bootstrap in charge of an invariant it cannot enforce.
//  2. Direction. pkg/events keeps its hooks as plain function fields
//     precisely so the bus layer never depends on the observability layer
//     (see events.Hooks). pkg/k8s is that layer's service-facing half;
//     pointing it at pkg/obs would make every consumer of pkg/k8s -- including
//     pkg/crdcheck and the envtest helpers -- drag in the OpenTelemetry SDK.
//
// The cost of the choice is that a new service can forget the call. That is
// exactly how the propagation came to exist and never run in production
// (Task C1 installed the hooks in pkg/events; no call site passed them), so
// cmd/clustarr's TestEveryServicePassesBusHooks parses every service's run.go
// and fails when a ConnectBus call site omits them.
func WithBusHooks(h events.Hooks) BusOption { return natsbus.WithHooks(h) }

// ConnectBus dials NATS and wraps the connection in a JetStream bus.
//
// service is the field-manager-style service name; it becomes the NATS client
// name, so `nats server report connections` names the pod's service and
// version rather than an anonymous connection.
//
// opts are passed through to the bus constructor. Every service is expected
// to pass [WithBusHooks] with pkg/obs.BusHooks(); see that function for why
// it is not the default.
//
// The connection reconnects forever. Dropping the bus is not fatal to a
// controller -- §3 puts Kubernetes watches first and the bus second -- so the
// process stays up and [BusReadyChecker] reports it unready until JetStream
// answers again.
func ConnectBus(url, service string, opts ...BusOption) (*natsbus.Bus, *nats.Conn, error) {
	if url == "" {
		return nil, nil, fmt.Errorf("k8s: empty NATS url")
	}
	nc, err := nats.Connect(url,
		nats.Name(fmt.Sprintf("clustarr-%s@%s", service, version.String())),
		nats.Timeout(BusConnectTimeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(BusReconnectWait),
		nats.RetryOnFailedConnect(true),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s: connect to NATS at %s: %w", url, err)
	}
	bus, err := natsbus.New(nc, opts...)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("k8s: open jetstream: %w", err)
	}
	return bus, nc, nil
}

// BusTopology is the topology this process should install: the default from
// pkg/events, collapsed to a single replica when [Options.BusSingleNode] is
// set.
func (o Options) BusTopology() events.Topology {
	t := events.Default()
	if o.BusSingleNode {
		return t.ForSingleNode()
	}
	return t
}

// EnsureTopology creates the streams, consumers and buckets in t.
//
// Every replica of every service calls this at startup: it is idempotent by
// design (pkg/events documents it as safe to run from every replica), and
// making each service responsible for the topology it uses is what keeps the
// chart from having to know the stream layout.
func EnsureTopology(ctx context.Context, bus events.Bus, t events.Topology) error {
	if bus == nil {
		return fmt.Errorf("k8s: nil bus")
	}
	ctx, cancel := context.WithTimeout(ctx, BusConnectTimeout)
	defer cancel()
	if err := bus.Ensure(ctx, t); err != nil {
		return fmt.Errorf("k8s: ensure JetStream topology: %w", err)
	}
	return nil
}

// BusReadyChecker is §13's "JetStream ping (all)" readiness gate: the pod is
// ready only while its NATS connection is up and JetStream answers.
//
// It deliberately does not become a liveness check. A NATS outage would then
// restart every controller in the cluster at once, and a controller whose
// Kubernetes watches are healthy has useful work to do regardless.
func BusReadyChecker(nc *nats.Conn, bus *natsbus.Bus) healthz.Checker {
	return func(req *http.Request) error {
		if nc == nil || bus == nil {
			return fmt.Errorf("NATS is not configured")
		}
		if !nc.IsConnected() {
			return fmt.Errorf("NATS is not connected (status %s)", nc.Status())
		}
		ctx := req.Context()
		if _, err := bus.JetStream().AccountInfo(ctx); err != nil {
			return fmt.Errorf("JetStream ping: %w", err)
		}
		return nil
	}
}
