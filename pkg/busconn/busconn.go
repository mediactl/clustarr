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

// Package busconn connects a process to the JetStream bus: Connect, the
// trace-hook option, the readiness check, EnsureTopology and its two
// leader-only keepers, KeepTopology and WatchDeadLetters, for the manager,
// and AwaitTopology for everyone else (spec §4.3 step 1.3, §5.9). It imports no
// pkg/k8s and no pkg/obs, so cmd/ui links it.
package busconn

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/version"
)

// Bus connection settings shared by every process.
const (
	// ConnectTimeout bounds the initial dial. A service that cannot reach
	// JetStream at startup still starts: it comes up unready and keeps
	// reconnecting, which is better than crash-looping a controller that also
	// has Kubernetes work to do.
	ConnectTimeout = 10 * time.Second

	// EnsureTimeout bounds one pass of EnsureTopology: forty streams,
	// consumers, buckets and object stores created or updated in sequence,
	// each a JetStream API call that waits on Raft. Against a cluster that
	// has just formed, with restored streams catching up to R3, one pass
	// outran ConnectTimeout (talos, 2026-10-16: "ensure stream
	// CLUSTARR_RELEASES: context deadline exceeded"), and KeepTopology
	// only retries a whole pass.
	EnsureTimeout = 2 * time.Minute

	// ReconnectDelayMin and ReconnectDelayMax bound the reconnect backoff
	// (ReconnectDelay): 250 ms doubling to 30 s, with full jitter, after
	// each pass over the server pool (ADR-0020 §6). It replaced a fixed
	// 2 s ReconnectWait, which sent every pod back at once after an outage.
	ReconnectDelayMin = 250 * time.Millisecond
	ReconnectDelayMax = 30 * time.Second

	// ReconnectBufSize is how many bytes of publishes nats.go buffers while
	// it reconnects: 8 MiB, nats.go's own default, named so it is a choice
	// (ADR-0020 §6). A publish past it fails with
	// nats.ErrReconnectBufExceeded, which natsbus reports as
	// events.ErrTransient: the caller's backoff sends it again.
	ReconnectBufSize = 8 << 20

	// DefaultNATSURL is the in-cluster address of the JetStream service the
	// umbrella chart installs.
	DefaultNATSURL = "nats://clustarr-nats:4222"
)

// Option configures the JetStream bus [Connect] builds. It is natsbus.Option
// under another name so a process does not have to import the concrete bus
// package just to pass an option through.
type Option = natsbus.Option

// WithHooks installs the observability hooks h on the bus.
//
// This package deliberately does NOT default them to pkg/obs.BusHooks(), and
// deliberately does not import pkg/obs at all. Two reasons, in order:
//
//  1. Ordering. tracing.Inject and tracing.Extract read and write through the
//     otel GLOBAL propagator and TracerProvider, which obs.Bootstrap installs.
//     Hooks wired before Bootstrap has run are a silent no-op -- a nil
//     propagator injects nothing. Run already calls Bootstrap and then
//     Connect, in that order, in one visible sequence; a default installed
//     inside Connect would put a package that knows nothing about Bootstrap
//     in charge of an invariant it cannot enforce.
//  2. Direction. pkg/events keeps its hooks as plain function fields
//     precisely so the bus layer never depends on the observability layer
//     (see events.Hooks). pkg/busconn is that layer's process-facing half;
//     pointing it at pkg/obs would make every consumer of pkg/busconn --
//     including pkg/k8s, pkg/crdcheck and the envtest helpers -- drag in the
//     OpenTelemetry SDK.
//
// The cost of the choice is that a new service can forget the call. That is
// exactly how the propagation came to exist and never run in production
// (Task C1 installed the hooks in pkg/events; no call site passed them), so
// cmd/clustarr's TestEveryServicePassesBusHooks parses every service's run.go
// and fails when a busconn.Connect call site omits them.
func WithHooks(h events.Hooks) Option { return natsbus.WithHooks(h) }

// Connect dials NATS and wraps the connection in a JetStream bus.
//
// service is the field-manager-style service name; it becomes the NATS client
// name, clustarr-<service>@<version>, so `nats server report connections`
// names the pod's service and version rather than an anonymous connection.
//
// opts are passed through to the bus constructor. Every service is expected
// to pass [WithHooks] with pkg/obs.BusHooks(); see that function for why it is
// not the default.
//
// The connection reconnects forever. Dropping the bus is not fatal to a
// controller -- §3 puts Kubernetes watches first and the bus second -- so the
// process stays up and [ReadyChecker] reports it unready until JetStream
// answers again.
func Connect(url, service string, opts ...Option) (*natsbus.Bus, *nats.Conn, error) {
	if url == "" {
		return nil, nil, fmt.Errorf("busconn: empty NATS url")
	}
	if err := ValidateURL(url); err != nil {
		return nil, nil, err
	}
	nopts := append([]nats.Option{
		nats.Name(fmt.Sprintf("clustarr-%s@%s", service, version.String())),
		nats.Timeout(ConnectTimeout),
		nats.RetryOnFailedConnect(true),
	}, ReconnectOptions()...)
	nc, err := nats.Connect(url, nopts...)
	if err != nil {
		return nil, nil, fmt.Errorf("busconn: connect to NATS at %s: %w", url, err)
	}
	bus, err := natsbus.New(nc, opts...)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("busconn: open jetstream: %w", err)
	}
	return bus, nc, nil
}

// EnsureTopology creates or updates every stream, consumer, dead-letter
// watcher, bucket and object store in t. Only the manager calls it, once
// before it starts and then through KeepTopology (spec §5.9); every other
// process waits for the topology with AwaitTopology and binds, so an older
// agent can never revert a newer topology. Until Wave 5 lands cmd/manager,
// every app/<svc>.Run still calls it through pkg/k8s's wrapper.
func EnsureTopology(ctx context.Context, bus events.Bus, t events.Topology) error {
	if bus == nil {
		return fmt.Errorf("busconn: nil bus")
	}
	ctx, cancel := context.WithTimeout(ctx, EnsureTimeout)
	defer cancel()
	if err := bus.Ensure(ctx, t); err != nil {
		return fmt.Errorf("busconn: ensure JetStream topology: %w", err)
	}
	return nil
}

// Checker is a health check: controller-runtime's healthz.Checker, spelled
// as its underlying type so this package links no controller-runtime and
// cmd/markers and cmd/transcode can shut their bus down through it
// (ADR-0020 §1). A Checker is assignable to healthz.Checker.
type Checker = func(*http.Request) error

// WedgeChecker is the `bus` liveness check (split §3.3 as amended
// 2026-10-07): it fails while a subscription of the process has sat at its
// lapsed cap past its handler budget plus its first-delivery deadline, which
// only handlers that ignore their context cause -- a local fault a restart
// cures. It reads the bus's own state, never the broker, so a NATS outage
// never fails it. A nil reporter always passes.
func WedgeChecker(r events.WedgeReporter) Checker {
	return func(*http.Request) error {
		if r == nil {
			return nil
		}
		return r.Wedged()
	}
}

// ReadyChecker is §13's "JetStream ping (all)" readiness gate: the pod is
// ready only while its NATS connection is up and JetStream answers.
//
// It deliberately does not become a liveness check. A NATS outage would then
// restart every controller in the cluster at once, and a controller whose
// Kubernetes watches are healthy has useful work to do regardless.
func ReadyChecker(nc *nats.Conn, bus *natsbus.Bus) Checker {
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
