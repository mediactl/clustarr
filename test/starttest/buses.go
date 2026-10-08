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

package starttest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// MemBus is an in-memory bus holding the single-node default topology.
func MemBus(t testing.TB) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus
}

// JetStreamBus boots an embedded JetStream server with the single-node
// default topology. Use it where membus cannot stand in (the dead-letter
// reader, the server's own key grammar and limits).
func JetStreamBus(t testing.TB) *natsbus.Bus {
	t.Helper()
	nc, err := nats.Connect(EmbeddedNATS(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus
}

// EnsureTopology does what the manager does at start (busconn.EnsureTopology
// over the single-node default topology) on the server at natsURL, so a
// test that runs only an agent, markers or transcode has a topology for its
// AwaitTopology to find. It closes its own connection before returning.
func EnsureTopology(t testing.TB, natsURL string) {
	t.Helper()
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err, "connect to %s", natsURL)
	defer nc.Close()
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	defer func() { _ = bus.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()), "ensure the topology on %s", natsURL)
}

// RecordingBus records the durable of every subscription made through it.
type RecordingBus struct {
	events.Bus
	mu       sync.Mutex
	durables []string
}

// Subscribe records s.Durable and subscribes through the wrapped bus.
func (b *RecordingBus) Subscribe(ctx context.Context, s events.Subscription, h events.Handler) (func(), error) {
	b.mu.Lock()
	b.durables = append(b.durables, s.Durable)
	b.mu.Unlock()
	return b.Bus.Subscribe(ctx, s, h)
}

// Subscribed is every durable subscribed so far, in order.
func (b *RecordingBus) Subscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.durables)
}
