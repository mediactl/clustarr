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

// This file exists for the reason pkg/events/natsbus/kvkey_contract_test.go
// gives at the top of its own: an illegal KV key has shipped twice, both
// times past suites that ran only against the in-memory bus, which has no
// key grammar. engine.ProgressKey mints a new key shape, so it is proved
// against a real embedded NATS server with the production topology applied.
package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// startServer boots an embedded JetStream server under the test's temporary
// directory -- the helper app/caption/throttle's contract test copies from
// pkg/events/natsbus for the same import-cycle reason.
func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-grabarr-engine",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func progressBucket(t *testing.T) events.KV {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketProgress)
}

// A Download UID is a Kubernetes UUID in every real cluster, but nothing in
// the Go types says so; the key must survive Put, Get and Delete for anything.
func TestProgressKeyIsAcceptedByARealServer(t *testing.T) {
	kv := progressBucket(t)
	ctx := t.Context()

	for _, uid := range []string{
		"6f1c7e0e-3b4a-4f7e-9a51-0c2d6f4b8e11", "tt0113277:2", "Amélie", "50%", "a,b", "with space",
		"tmdb/949", "", "a\tb", `quote"and'apos`, "\x00", ".leading", "trailing.", "a..b",
	} {
		t.Run(uid, func(t *testing.T) {
			key := engine.ProgressKey(uid)
			require.True(t, events.ValidKVKey(key), "ValidKVKey rejected %q", key)
			_, err := kv.Put(ctx, key, []byte("v"))
			require.NoError(t, err, "a real server rejected the key %q", key)
			got, err := kv.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, []byte("v"), got.Value)
			require.NoError(t, kv.Delete(ctx, key), "Delete rejected the key %q", key)
		})
	}
}

// The publisher end to end against the real clustarr-progress bucket: what
// it writes is a schema.DownloadProgress under the Download's key.
func TestProgressPublisherWritesTheRealBucket(t *testing.T) {
	kv := progressBucket(t)
	ctx := context.Background()
	lc := &listClient{}
	lc.set(download.Item{ID: "aaaa", Status: download.StatusDownloading, TotalBytes: 100, DownloadedBytes: 50})
	p := &engine.ProgressPublisher{
		Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
			WithObjects(progressDownload("real", "6f1c7e0e-3b4a-4f7e-9a51-0c2d6f4b8e11", "t-0", "aaaa")).Build(),
		Download: lc, EngineID: "t-0", KV: kv,
	}
	require.NoError(t, p.PublishOnce(ctx))

	entry, err := kv.Get(ctx, engine.ProgressKey("6f1c7e0e-3b4a-4f7e-9a51-0c2d6f4b8e11"))
	require.NoError(t, err)
	var got schema.DownloadProgress
	require.NoError(t, json.Unmarshal(entry.Value, &got))
	require.Equal(t, "real", got.DownloadRef.Name)
	require.EqualValues(t, 50_000, got.PercentMilli)
}
