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

// ConnectBus dials NATS and wraps the connection in a JetStream bus.
//
// service is the field-manager-style service name; it becomes the NATS client
// name, so `nats server report connections` names the pod's service and
// version rather than an anonymous connection.
//
// The connection reconnects forever. Dropping the bus is not fatal to a
// controller -- §3 puts Kubernetes watches first and the bus second -- so the
// process stays up and [BusReadyChecker] reports it unready until JetStream
// answers again.
func ConnectBus(url, service string) (*natsbus.Bus, *nats.Conn, error) {
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
	bus, err := natsbus.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("k8s: open jetstream: %w", err)
	}
	return bus, nc, nil
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
