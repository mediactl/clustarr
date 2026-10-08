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

package busconn

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

func keepEvery(t *testing.T, d time.Duration) {
	t.Helper()
	old := keepInterval
	keepInterval = d
	t.Cleanup(func() { keepInterval = old })
}

func missingOf(ctx context.Context, t *testing.T, bus events.Bus, top events.Topology) []string {
	t.Helper()
	m, err := bus.(events.StreamAdmin).Missing(ctx, top)
	require.NoError(t, err)
	return m
}

// The manager ensures once when it wins the lease, and re-creates whatever
// goes missing within an interval (spec §5.9).
func TestKeepTopologyEnsuresAtTheLeaseAndReplacesWhatGoesMissing(t *testing.T) {
	keepEvery(t, 50*time.Millisecond)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	top := events.Default().ForSingleNode()
	require.NotEmpty(t, missingOf(t.Context(), t, bus, top), "nothing exists before the lease")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- KeepTopology(ctx, nil, bus, top) }()

	require.Eventually(t, func() bool { return len(missingOf(ctx, t, bus, top)) == 0 },
		5*time.Second, 20*time.Millisecond, "ensured when the lease was won")
	require.NoError(t, bus.DeleteSubscription(ctx, events.StreamWorkIndex, events.ConsumerIndexRSS))
	require.NotEmpty(t, missingOf(ctx, t, bus, top))
	require.Eventually(t, func() bool { return len(missingOf(ctx, t, bus, top)) == 0 },
		5*time.Second, 20*time.Millisecond, "re-created what went missing")
	cancel()
	require.NoError(t, <-done, "losing the lease is no error")
}

type countingBus struct {
	*natsbus.Bus
	ensures atomic.Int32
}

func (b *countingBus) Ensure(ctx context.Context, t events.Topology) error {
	b.ensures.Add(1)
	return b.Bus.Ensure(ctx, t)
}

func runServer(t *testing.T, dir string, port int) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "busconn-keep", Host: "127.0.0.1", Port: port,
		JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

// A single-node NATS restart wipes every memory-backed stream and its
// consumers. Only the manager re-creates topology, and it does so on the
// reconnect, without waiting for its interval (spec §5.9, §9.8); the
// connection's own reconnect handler still runs.
func TestKeepTopologyReEnsuresWhenNATSReconnects(t *testing.T) {
	keepEvery(t, time.Hour) // only the reconnect may re-ensure
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv := runServer(t, dir, -1)
	port := srv.Addr().(*net.TCPAddr).Port
	var prevRan atomic.Int32
	nc, err := nats.Connect(srv.ClientURL(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond),
		nats.ReconnectHandler(func(*nats.Conn) { prevRan.Add(1) }))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	nb, err := natsbus.New(nc)
	require.NoError(t, err)
	bus := &countingBus{Bus: nb}
	t.Cleanup(func() { _ = bus.Close() })
	top := events.Default().ForSingleNode()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = KeepTopology(ctx, nc, bus, top) }()
	require.Eventually(t, func() bool { return bus.ensures.Load() == 1 && len(missingOf(ctx, t, bus, top)) == 0 },
		20*time.Second, 50*time.Millisecond)

	srv.Shutdown()
	srv.WaitForShutdown()
	runServer(t, dir, port) // memory-backed streams, consumers and buckets are gone
	require.Eventually(t, func() bool { return bus.ensures.Load() == 2 && len(missingOf(ctx, t, bus, top)) == 0 },
		30*time.Second, 100*time.Millisecond, "re-ensured on the reconnect")
	require.Equal(t, int32(1), prevRan.Load(), "the connection's own reconnect handler is chained, not replaced")
}

type adminlessBus struct{ events.Bus }

func TestKeepTopologyNeedsABusThatCanReportWhatIsMissing(t *testing.T) {
	err := KeepTopology(t.Context(), nil, adminlessBus{}, events.Default())
	require.ErrorContains(t, err, "cannot report")
}
