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
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// The kind cluster's NATS (config/nats, the chart's nats.config.jetstream)
// caps the memory store at 256Mi and the file store at 10Gi. The 5 GiB
// artwork object store was mapped to memory storage by ForSingleNode and
// every controller crash-looped on "insufficient memory resources
// available" (2026-09-24); TestEnsureDefaultTopology never saw it because
// its embedded server has no memory ceiling. This one has the cluster's.

const (
	kindMaxMemoryStore = 256 << 20
	kindMaxFileStore   = 10 << 30
)

func startServerWithKindLimits(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName:         "clustarr-kind-limits",
		Host:               "127.0.0.1",
		Port:               -1,
		JetStream:          true,
		JetStreamMaxMemory: kindMaxMemoryStore,
		JetStreamMaxStore:  kindMaxFileStore,
		StoreDir:           dir,
		NoLog:              true,
		NoSigs:             true,
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

func TestEnsureSingleNodeTopologyFitsTheKindServersLimits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srv := startServerWithKindLimits(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	defer func() { _ = bus.Close() }()

	top := events.Default().ForSingleNode()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure on a server with a %d-byte memory store: %v", int64(kindMaxMemoryStore), err)
	}

	// The artwork bucket landed on the file store, at its full size.
	store, err := bus.JetStream().ObjectStore(ctx, events.BucketArtwork)
	if err != nil {
		t.Fatalf("object store %s: %v", events.BucketArtwork, err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Storage() != jetstream.FileStorage {
		t.Fatalf("artwork object store storage = %v, want file", status.Storage())
	}
	if got := status.(*jetstream.ObjectBucketStatus).StreamInfo().Config.MaxBytes; got != events.ArtworkMaxBytes {
		t.Fatalf("artwork object store MaxBytes = %d, want %d", got, events.ArtworkMaxBytes)
	}
}
