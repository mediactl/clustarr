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

	"github.com/nats-io/nats.go"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// The bus connector lives in pkg/busconn (spec §4.3 step 1.3), which cmd/ui
// links without pkg/k8s. These wrappers keep the app/<svc> roots and
// cmd/clustarr compiling until Wave 5 deletes them. New code calls pkg/busconn.

// BusOption is busconn.Option.
type BusOption = busconn.Option

// WithBusHooks is busconn.WithHooks.
func WithBusHooks(h events.Hooks) BusOption { return busconn.WithHooks(h) }

// ConnectBus is busconn.Connect.
func ConnectBus(url, service string, opts ...BusOption) (*natsbus.Bus, *nats.Conn, error) {
	return busconn.Connect(url, service, opts...)
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

// EnsureTopology is busconn.EnsureTopology.
func EnsureTopology(ctx context.Context, bus events.Bus, t events.Topology) error {
	return busconn.EnsureTopology(ctx, bus, t)
}

// BusReadyChecker is busconn.ReadyChecker.
func BusReadyChecker(nc *nats.Conn, bus *natsbus.Bus) healthz.Checker {
	return busconn.ReadyChecker(nc, bus)
}
