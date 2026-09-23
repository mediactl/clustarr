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

package scenemap

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestCacheKeysAreKeysARealNATSServerAccepts puts every key shape Cached
// builds to a real embedded JetStream KV bucket. Cached is handed the
// gateway's tiered cache, whose L2 is NATS KV, and CLAUDE.md records two
// escapes of a key the in-memory bus accepted and NATS did not; a regex
// restated here is exactly where both hid, so the server is the oracle.
func TestCacheKeysAreKeysARealNATSServerAccepts(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "scenemap-keygrammar", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "clustarr-scenemap-contract"})
	require.NoError(t, err)

	keys := []string{keyHaveMap, keyNames, keyMappings(1), keyMappings(72454), keyMappings(9223372036854775807)}
	seen := map[string]bool{}
	for _, k := range keys {
		require.False(t, seen[k], "key %q built twice", k)
		seen[k] = true
		_, err := kv.Put(ctx, k, []byte("{}"))
		require.NoError(t, err, "NATS rejected key %q", k)
		require.NoError(t, kv.Delete(ctx, k), "NATS rejected deleting key %q", k)
	}
}
