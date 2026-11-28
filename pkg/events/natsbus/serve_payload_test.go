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
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestServeReportsAReplyTheServerRefuses pins the failure found on the
// owner's cluster on 2026-09-24: index answered rpc.index.download
// with a 1.3 MB .nzb, the Helm chart's NATS ran the server's 1 MiB
// max_payload default, nats.go refused the reply client-side with
// ErrMaxPayload, Serve discarded that error, and the engine waited out its
// whole 45 s deadline on every attempt with nothing to say why. A reply the
// bus will not send -- since 2026-11-27 one over events.MaxReplyBytes, the
// most a reply in parts may span -- must come back to the requester as a
// service error that names the cause, at once, not as a timeout.
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

	body := bytes.Repeat([]byte("x"), events.MaxReplyBytes+1)
	stop, err := bus.Serve(events.RPCIndexDownload, events.QueueGroupIndex,
		func(context.Context, []byte) ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	var out map[string]any
	err = bus.Request(ctx, events.RPCIndexDownload, map[string]string{"guid": "g"}, &out)
	elapsed := time.Since(start)

	switch {
	case err == nil:
		t.Fatal("Request returned no error for a reply over events.MaxReplyBytes")
	case errors.Is(err, context.DeadlineExceeded):
		t.Fatalf("Request timed out after %s: the dropped reply was not reported", elapsed)
	case !strings.Contains(err.Error(), nats.ErrMaxPayload.Error()):
		t.Fatalf("Request error = %q, want it to name %q", err, nats.ErrMaxPayload)
	case elapsed > 5*time.Second:
		t.Fatalf("Request took %s to fail; the error reply should be immediate", elapsed)
	}
}

// TestAFullDownloadWireBudgetCrossesTheBrokerInParts sends the largest
// rpc.index.download reply index will send -- schema.MaxDownloadWireBytes
// of incompressible body, base64 in JSON -- through a real server capped at
// the broker's 8Mi (config/nats), and requires it to arrive whole. It is
// five times one message's room, so it crosses in parts; this is the only
// test that sees the real framing.
func TestAFullDownloadWireBudgetCrossesTheBrokerInParts(t *testing.T) {
	const brokerMaxPayload = 8 << 20

	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-download-budget", Host: "127.0.0.1", Port: -1,
		MaxPayload: brokerMaxPayload, NoLog: true, NoSigs: true,
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
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	body := make([]byte, schema.MaxDownloadWireBytes)
	r := rand.New(rand.NewPCG(5, 6))
	for i := range body {
		body[i] = byte(r.Uint32())
	}
	reply, err := json.Marshal(schema.DownloadResponse{
		Bytes: body, ContentType: "application/x-nzb", Encoding: schema.DownloadEncodingGzip,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	stop, err := bus.Serve(events.RPCIndexDownload, events.QueueGroupIndex,
		func(context.Context, []byte) ([]byte, error) { return reply, nil })
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got schema.DownloadResponse
	if err := bus.Request(ctx, events.RPCIndexDownload, schema.DownloadRequest{GUID: "g"}, &got); err != nil {
		t.Fatalf("a full-budget reply (%d bytes of JSON) did not cross an 8Mi broker: %v", len(reply), err)
	}
	if !bytes.Equal(got.Bytes, body) {
		t.Fatal("the reply's body changed on the way")
	}
	if len(reply) <= brokerMaxPayload {
		t.Fatalf("the fixture is %d bytes: it must be over one message's room to cross in parts", len(reply))
	}
	if len(reply) > events.MaxReplyBytes {
		t.Fatalf("a full-budget reply is %d bytes of JSON, over events.MaxReplyBytes (%d)", len(reply), events.MaxReplyBytes)
	}
}

// smallBroker is a bus on a server whose max_payload is maxPayload.
func smallBroker(t *testing.T, maxPayload int32, opts ...natsbus.Option) (*natsbus.Bus, *nats.Conn) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-reply-parts", Host: "127.0.0.1", Port: -1,
		MaxPayload: maxPayload, NoLog: true, NoSigs: true,
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
	bus, err := natsbus.New(nc, opts...)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus, nc
}

// A reply larger than one message crosses in parts and arrives byte for
// byte: at one message's room exactly, one byte over it, a whole number of
// parts and a ragged last one. A 153 GB season pack's .nzb gzipped to 6.14 MB
// against the 6.03 one message held, was asked for again and again, and its
// indexer refused the release for a day (talos, 2026-11-27).
func TestAReplyLargerThanOneMessageCrossesInParts(t *testing.T) {
	const maxPayload = 64 << 10
	bus, _ := smallBroker(t, maxPayload)

	r := rand.New(rand.NewPCG(7, 8))
	for _, size := range []int{maxPayload - 8<<10, maxPayload, maxPayload + 1, 4 * maxPayload, 7*maxPayload + 13, 40 * maxPayload} {
		body := make([]byte, size)
		for i := range body {
			body[i] = byte(r.Uint32())
		}
		subject := "rpc.test.parts." + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
		stop, err := bus.Serve(subject, "test",
			func(context.Context, []byte) ([]byte, error) { return body, nil })
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var got []byte
		err = bus.Request(ctx, subject, []byte("q"), &got)
		cancel()
		stop()
		if err != nil {
			t.Fatalf("a reply of %d bytes did not cross a %d-byte broker: %v", size, maxPayload, err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("a reply of %d bytes arrived as %d bytes, or changed", size, len(got))
		}
	}
}

// A requester that reads the first part and goes away costs the responder
// its handler slot no longer than that caller would have waited: the next
// request is served.
func TestAReplyInPartsNobodyFinishesReadingFreesItsHandler(t *testing.T) {
	const maxPayload = 64 << 10
	bus, nc := smallBroker(t, maxPayload, natsbus.WithServeLimits(events.ServeLimits{Concurrency: 1, Queue: 1}))

	body := bytes.Repeat([]byte("p"), 5*maxPayload)
	stop, err := bus.Serve("rpc.test.abandoned", "test",
		func(context.Context, []byte) ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(stop)

	// A caller that will wait one second, speaking the wire directly, takes
	// the first part and asks for no more.
	msg := &nats.Msg{Subject: "rpc.test.abandoned", Data: []byte("q"), Header: nats.Header{}}
	msg.Header.Set(events.HeaderTimeout, "1000")
	first, err := nc.RequestMsg(msg, 5*time.Second)
	if err != nil {
		t.Fatalf("first part: %v", err)
	}
	if first.Header.Get("Clustarr-Reply-Parts") == "" {
		t.Fatal("the reply did not come in parts")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []byte
	if err := bus.Request(ctx, "rpc.test.abandoned", []byte("q"), &got); err != nil {
		t.Fatalf("the responder's one slot was not freed for the next request: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("the next request's reply changed on the way")
	}
}
