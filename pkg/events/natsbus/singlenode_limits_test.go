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
// caps the memory store at 256Mi and the file store at 20Gi (config/nats
// sets max_file_store 20Gi since the records buckets, loop spec §4.3). The
// 5 GiB artwork object store was mapped to memory storage by ForSingleNode
// and every controller crash-looped on "insufficient memory resources
// available" (2026-09-24); TestEnsureDefaultTopology never saw it because
// its embedded server has no memory ceiling. This one has the cluster's.
const (
	kindMaxMemoryStore = 256 << 20
	kindMaxFileStore   = 20 << 30
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

	// So did the extended metadata and Plex extras buckets, which must
	// survive a restart.
	for _, name := range []string{events.BucketMetadataExtended, events.BucketPlexExtras} {
		kv, err := bus.JetStream().KeyValue(ctx, name)
		if err != nil {
			t.Fatalf("bucket %s: %v", name, err)
		}
		kvStatus, err := kv.Status(ctx)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if got := kvStatus.(*jetstream.KeyValueBucketStatus).StreamInfo().Config.Storage; got != jetstream.FileStorage {
			t.Fatalf("%s storage = %v, want file", name, got)
		}
	}

	// Every records bucket -- the loop's four and ADR-0019's six
	// agent-written ones -- lands on the file store with its caps (loop spec
	// §4.3, §4.15; A1.6).
	records := 0
	for _, b := range top.Buckets {
		if !b.Records {
			continue
		}
		records++
		kv, err := bus.JetStream().KeyValue(ctx, b.Name)
		if err != nil {
			t.Fatalf("bucket %s: %v", b.Name, err)
		}
		st, err := kv.Status(ctx)
		if err != nil {
			t.Fatalf("status %s: %v", b.Name, err)
		}
		cfg := st.(*jetstream.KeyValueBucketStatus).StreamInfo().Config
		if cfg.Storage != jetstream.FileStorage || cfg.MaxBytes != b.MaxBytes || cfg.MaxMsgSize != b.MaxValueSize {
			t.Errorf("records bucket %s = storage %v, MaxBytes %d, MaxMsgSize %d; want file, %d, %d",
				b.Name, cfg.Storage, cfg.MaxBytes, cfg.MaxMsgSize, b.MaxBytes, b.MaxValueSize)
		}
	}
	if records < 10 {
		t.Errorf("%d records buckets in the topology, want the loop's 4 and the agents' 6", records)
	}

	// The A1.6 durable streams (intake, engine) stay on file at full size;
	// CLUSTARR_TASK_EVENTS is a memory stream inside the budget.
	for _, name := range []string{events.StreamIntake, events.StreamWorkEngine, events.StreamWorkProbe} {
		st, err := bus.JetStream().Stream(ctx, name)
		if err != nil {
			t.Fatalf("stream %s: %v", name, err)
		}
		spec, _ := top.Stream(name)
		if cfg := st.CachedInfo().Config; cfg.Storage != jetstream.FileStorage || cfg.MaxBytes != spec.MaxBytes {
			t.Errorf("stream %s = storage %v, MaxBytes %d; want file, %d", name, cfg.Storage, cfg.MaxBytes, spec.MaxBytes)
		}
	}

	// What the server reserves is what the topology asks for, and it fits:
	// file streams, file buckets and object stores against the 20 GiB file
	// store, memory streams and buckets against the 256 MiB memory store.
	var fileStreams, fileBuckets, objectStores, memory int64
	for _, s := range top.Streams {
		if s.Storage == events.StorageFile {
			fileStreams += max(s.MaxBytes, 0)
		} else {
			memory += max(s.MaxBytes, 0)
		}
	}
	for _, b := range top.Buckets {
		if b.Storage == events.StorageFile {
			fileBuckets += max(b.MaxBytes, 0)
		} else {
			memory += max(b.MaxBytes, 0)
		}
	}
	for _, o := range top.ObjectStores {
		objectStores += max(o.MaxBytes, 0)
	}
	acct, err := bus.JetStream().AccountInfo(ctx)
	if err != nil {
		t.Fatalf("account info: %v", err)
	}
	t.Logf("file reservations: streams %d MiB, buckets %d MiB, object stores %d MiB = %d MiB of %d MiB; memory %d MiB of %d MiB",
		fileStreams>>20, fileBuckets>>20, objectStores>>20, (fileStreams+fileBuckets+objectStores)>>20,
		int64(kindMaxFileStore)>>20, memory>>20, int64(kindMaxMemoryStore)>>20)
	if want := uint64(fileStreams + fileBuckets + objectStores); acct.ReservedStore != want {
		t.Errorf("the server reserves %d file bytes, the topology asks for %d", acct.ReservedStore, want)
	}
	if acct.ReservedStore > kindMaxFileStore || acct.ReservedMemory > kindMaxMemoryStore {
		t.Errorf("reserved file %d of %d, memory %d of %d", acct.ReservedStore, int64(kindMaxFileStore),
			acct.ReservedMemory, int64(kindMaxMemoryStore))
	}
}
