/*
Copyright 2026 The clustarr Authors.

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

package natsbus_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestServeReportsAReplyTheServerRefuses pins the failure found on the
// owner's cluster on 2026-09-24: indexarr answered rpc.indexarr.download
// with a 1.3 MB .nzb, the Helm chart's NATS ran the server's 1 MiB
// max_payload default, nats.go refused the reply client-side with
// ErrMaxPayload, Serve discarded that error, and the engine waited out its
// whole 45 s deadline on every attempt with nothing to say why. A reply the
// connection cannot send must come back to the requester as a service
// error that names the cause, at once, not as a timeout.
func TestServeReportsAReplyTheServerRefuses(t *testing.T) {
	const maxPayload = 64 << 10

	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-max-payload",
		Host:       "127.0.0.1",
		Port:       -1,
		MaxPayload: maxPayload,
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	if got := nc.MaxPayload(); got != maxPayload {
		t.Fatalf("connection max payload = %d, want %d", got, maxPayload)
	}

	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	// Twice the limit: the JSON envelope alone would not tip a body that
	// is merely close, so this is unambiguously over.
	body := bytes.Repeat([]byte("x"), 2*maxPayload)
	err = bus.Serve(events.RPCIndexDownload, events.QueueGroupIndexarr,
		func(context.Context, []byte) ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	var out map[string]any
	err = bus.Request(ctx, events.RPCIndexDownload, map[string]string{"guid": "g"}, &out)
	elapsed := time.Since(start)

	switch {
	case err == nil:
		t.Fatal("Request returned no error for a reply twice the server's max_payload")
	case errors.Is(err, context.DeadlineExceeded):
		t.Fatalf("Request timed out after %s: the dropped reply was not reported", elapsed)
	case !strings.Contains(err.Error(), nats.ErrMaxPayload.Error()):
		t.Fatalf("Request error = %q, want it to name %q", err, nats.ErrMaxPayload)
	case elapsed > 5*time.Second:
		t.Fatalf("Request took %s to fail; the error reply should be immediate", elapsed)
	}
}
