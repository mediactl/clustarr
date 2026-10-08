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

package uicli

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// TestBuildBusLogsAnUnconnectedBus: an endpoint nothing listens on is not a
// busconn.Connect error (it retries in the background), so the only signal
// an operator gets is the one startup line naming the URL. A reachable
// endpoint logs nothing of the kind.
//
// Either way the bus buildBus hands back is whole -- every closure bound and
// close never nil -- and close is ADR-0020 decision 1's shutdown as built:
// busconn.Shutdown drains the bus (the ui serves no responder and binds no
// pull, so there is nothing before the connection) and then the connection,
// within busShutdownBudget, and returns without falling back to the hard
// Close. A connection still dialling an empty port has nothing to flush, so
// its drain ends at once; it must not wait out the budget. The fallback is
// the one thing the path logs, on the context's slog logger.
func TestBuildBusLogsAnUnconnectedBus(t *testing.T) {
	capture := func(t *testing.T, url string) (logged string, warned []string) {
		t.Helper()
		var (
			mu    sync.Mutex
			lines []string
		)
		sink := funcr.New(func(prefix, args string) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, prefix+" "+args)
		}, funcr.Options{})
		warns := &warnings{}
		ctx := log.IntoContext(logging.NewContext(context.Background(), slog.New(warns)), sink)

		b := buildBus(ctx, url)
		require.NotNil(t, b.artwork)
		require.NotNil(t, b.search)
		require.NotNil(t, b.extended)
		require.NotNil(t, b.extras)
		require.NotNil(t, b.progress)
		require.NotNil(t, b.transcode)
		require.NotNil(t, b.imports)
		require.NotNil(t, b.close)

		start := time.Now()
		b.close()
		require.Less(t, time.Since(start), busShutdownBudget/2,
			"close waited out the shutdown budget: the drain should end as soon as the connection closes")

		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n"), warns.lines()
	}

	t.Run("unreachable", func(t *testing.T) {
		url := "nats://" + freeAddress(t)
		logged, warned := capture(t, url)
		require.Contains(t, logged, "NATS is not connected yet")
		require.Contains(t, logged, url, "the line must name the endpoint being dialled")
		require.Empty(t, warned, "the shutdown of a connection that never connected fell back to Close")
	})

	t.Run("reachable", func(t *testing.T) {
		srv := startNATS(t)
		logged, warned := capture(t, srv.ClientURL())
		require.NotContains(t, logged, "not connected")
		require.Empty(t, warned, "the shutdown fell back to Close instead of draining")
		require.Eventually(t, func() bool { return srv.NumClients() == 0 }, 10*time.Second, 50*time.Millisecond,
			"close returned with the connection still open")
	})
}

// warnings is a slog.Handler recording every record at warn or above.
type warnings struct {
	mu  sync.Mutex
	got []string
}

func (w *warnings) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (w *warnings) Handle(_ context.Context, r slog.Record) error {
	line := r.Message
	r.Attrs(func(a slog.Attr) bool {
		line += " " + a.String()
		return true
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, line)
	return nil
}

func (w *warnings) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnings) WithGroup(string) slog.Handler      { return w }

func (w *warnings) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.got...)
}

// freeAddress asks the kernel for an unused port and hands back the address.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// startNATS runs an embedded JetStream server for the test's lifetime.
func startNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-uicli-bus-test",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	return srv
}
