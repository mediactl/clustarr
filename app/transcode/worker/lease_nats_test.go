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

package worker

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// leaseKVWithTTL is the clustarr-transcode-leases bucket of a real embedded
// JetStream server, built the way telemetry_test.go's progressKV builds
// clustarr-progress: NATS, not the in-memory bus, is the oracle for TTL
// expiry and Update's compare-and-swap (CLAUDE.md). ttl overrides the
// bucket's production TTL (events.TranscodeLeaseTTL, 90s) so the test does
// not run for minutes.
func leaseKVWithTTL(t *testing.T, ttl time.Duration) events.KV {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "squasharr-lease", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })

	topo := events.Default().ForSingleNode()
	for i := range topo.Buckets {
		if topo.Buckets[i].Name == events.BucketTranscodeLeases {
			topo.Buckets[i].TTL = ttl
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, bus.Ensure(ctx, topo))
	return bus.KV(events.BucketTranscodeLeases)
}

func TestLeaseBucketTTLIsExtendedByUpdate(t *testing.T) {
	kv := leaseKVWithTTL(t, 2*time.Second)
	ctx := context.Background()
	rev, err := kv.Create(ctx, "lease.j", []byte("held"))
	require.NoError(t, err)
	for i := 0; i < 4; i++ { // 4s of renewals, twice the TTL
		time.Sleep(time.Second)
		rev, err = kv.Update(ctx, "lease.j", []byte("held"), rev)
		require.NoError(t, err, "renewal %d", i)
	}
	_, err = kv.Create(ctx, "lease.j", []byte("other"))
	assert.ErrorIs(t, err, events.ErrKeyExists, "a renewed lease must still be held")
	time.Sleep(3 * time.Second) // no renewals past the TTL
	_, err = kv.Create(ctx, "lease.j", []byte("other"))
	assert.NoError(t, err, "a lapsed lease must be claimable: the server expires it")
}
