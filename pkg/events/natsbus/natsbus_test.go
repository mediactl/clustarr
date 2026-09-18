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

package natsbus_test

import (
	"context"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// startServer boots an embedded JetStream server with its store under the
// test's temporary directory, so the suite needs no external broker.
func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-contract",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
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
	return srv
}

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestBusContract(t *testing.T) {
	contracttest.RunBusContract(t, func() events.Bus {
		bus, err := natsbus.New(connect(t))
		if err != nil {
			t.Fatalf("natsbus.New: %v", err)
		}
		return bus
	})
}

// TestEnsureDefaultTopology applies the production topology, shrunk only to a
// single replica, so every stream, durable consumer and bucket in the design
// is validated against a real server.
func TestEnsureDefaultTopology(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	defer func() { _ = bus.Close() }()

	top := events.Default().ForSingleNode()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Ensure is idempotent: every replica runs it at startup.
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	js := bus.JetStream()
	for _, spec := range top.Streams {
		st, err := js.Stream(ctx, spec.Name)
		if err != nil {
			t.Fatalf("stream %s: %v", spec.Name, err)
		}
		cfg := st.CachedInfo().Config
		if cfg.Retention != events.StreamConfig(spec).Retention {
			t.Errorf("stream %s retention = %v", spec.Name, cfg.Retention)
		}
		if spec.Retention == events.RetentionWorkQueue && !cfg.AllowMsgSchedules {
			t.Errorf("work stream %s does not allow message schedules", spec.Name)
		}
	}
	for _, spec := range top.Consumers {
		c, err := js.Consumer(ctx, spec.Stream, spec.Name)
		if err != nil {
			t.Fatalf("consumer %s: %v", spec.Name, err)
		}
		cfg := c.CachedInfo().Config
		if cfg.MaxDeliver != spec.MaxDeliver {
			t.Errorf("consumer %s MaxDeliver = %d, want %d",
				spec.Name, cfg.MaxDeliver, spec.MaxDeliver)
		}
		if len(cfg.BackOff) != len(spec.BackOff) {
			t.Errorf("consumer %s BackOff = %v, want %v",
				spec.Name, cfg.BackOff, spec.BackOff)
		}
	}
	for _, spec := range top.Buckets {
		if _, err := js.KeyValue(ctx, spec.Name); err != nil {
			t.Fatalf("bucket %s: %v", spec.Name, err)
		}
	}
}

// TestEnsureRefusesRetentionChange pins the guard that keeps an operator from
// silently dropping queued work by flipping a work stream to Limits.
func TestEnsureRefusesRetentionChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	defer func() { _ = bus.Close() }()

	top := contracttest.Topology()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for i := range top.Streams {
		if top.Streams[i].Name == events.StreamWorkCatalogarr {
			top.Streams[i].Retention = events.RetentionLimits
		}
	}
	if err := bus.Ensure(ctx, top); err == nil {
		t.Fatal("Ensure accepted a changed work-stream retention")
	} else if !isRetentionErr(err) {
		t.Fatalf("Ensure error = %v, want ErrRetentionImmutable", err)
	}
}

func isRetentionErr(err error) bool {
	for e := err; e != nil; {
		if e == events.ErrRetentionImmutable {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
