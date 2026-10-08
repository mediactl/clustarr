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

package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui"
)

// TestUICommandClosesItsBusOnShutdown proves `ui` holds its artwork
// connection open for as long as its run runs, and shuts it down once the
// run returns rather than leaving it to process exit (the `clustarr all` leg
// went with `clustarr all`, §3.10). The server's own client count is the
// observation: nothing else in the command dials it.
//
// It also holds the command to ADR-0020 decision 1 as built: the shutdown is
// busconn.Shutdown's drain -- the bus's pulls, then its responders (the ui
// has neither), then the connection -- inside the command's budget, never
// the hard Close it falls back to once the budget is spent. That fallback is
// the one thing the path logs ("bus shutdown fell back to closing", on the
// command context's logger), so a clean drain logs no such line. (The
// unreachable kubeconfig makes the cluster reader warn too; those are not
// this test's subject.)
func TestUICommandClosesItsBusOnShutdown(t *testing.T) {
	useUnreachableKubeconfig(t)
	srv := startCountingNATS(t)

	var (
		mu           sync.Mutex
		whileRunning = -1
	)
	root := uicliCommandRecording(func(context.Context, ui.Options) error {
		mu.Lock()
		defer mu.Unlock()
		whileRunning = srv.NumClients()
		return nil
	})
	root.SetArgs([]string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous", "--nats-url", srv.ClientURL()})
	logs := &warnings{}
	ctx, cancel := context.WithCancel(logging.NewContext(context.Background(), slog.New(logs)))
	t.Cleanup(cancel)
	require.NoError(t, root.ExecuteContext(ctx))

	mu.Lock()
	got := whileRunning
	mu.Unlock()
	require.Equal(t, 1, got, "ui's bus was not connected while its run ran")
	waitFor(t, "ui's NATS connection to close after the run returned", func() bool {
		return srv.NumClients() == 0
	})
	for _, line := range logs.lines() {
		require.NotContains(t, line, "bus shutdown",
			"ui's bus shutdown fell back to Close: busconn.Shutdown drains the connection within the "+
				"command's budget (ADR-0020 §1), and only a spent budget or a failed drain aborts it")
	}
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

// startCountingNATS runs an embedded JetStream server and returns it, so a
// test can count the clients connected to it.
func startCountingNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-cmd-ui-bus-test",
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

// waitFor polls cond until it holds or 30s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
