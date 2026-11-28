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

package kvledger_test

import (
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/download/usenet/kvledger"
	"github.com/mediactl/clustarr/pkg/download/usenetaccount"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// buckets is the connection ledger's bucket and the usage bucket on one
// embedded server.
func buckets(t *testing.T) (conns, usage events.KV) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-kvledger-fleet", Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketUsenetConnections), bus.KV(events.BucketUsenetUsage)
}

const pool = "pool:plan:me@x"

// Two pods write their weights into a pool's entry; each one's share of
// the pool follows them, their shares never sum past the pool, and a pod
// that goes idle leaves the pool to the other.
func TestTwoPodsDivideAPoolByWeight(t *testing.T) {
	kv, _ := buckets(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 0)
	b := kvledger.New(kv, "pod-b", 0)

	total, err := a.Join(ctx, pool, 2000)
	require.NoError(t, err)
	require.Equal(t, int64(2000), total, "alone")
	total, err = b.Join(ctx, pool, 500)
	require.NoError(t, err)
	require.Equal(t, int64(2500), total)
	total, err = a.Join(ctx, pool, 2000)
	require.NoError(t, err)
	require.Equal(t, int64(2500), total, "a pod's own weight is replaced, not added again")

	entA, entB := usenet.Entitlement(100, 2000, 2500), usenet.Entitlement(100, 500, 2500)
	require.Equal(t, 80, entA)
	require.Equal(t, 20, entB)
	take := func(l *kvledger.Ledger, holder string, podLimit, want int) int {
		n, err := l.Take(ctx, pool, 100, usenet.Grant{Holder: holder, Server: "us", Host: "news.example", PodLimit: podLimit}, want, 1)
		require.NoError(t, err)
		return n
	}
	require.Equal(t, 50, take(a, "a/1", entA, 50))
	require.Equal(t, 30, take(a, "a/2", entA, 50), "pod a holds its 80 and no more")
	require.Equal(t, 0, take(a, "a/3", entA, 1))
	require.Equal(t, 20, take(b, "b/1", entB, 50), "pod b's 20 are left for it")

	// Pod b goes idle: its weight is zero, and the pool is pod a's.
	require.NoError(t, b.Give(ctx, pool, "b/1"))
	total, err = b.Join(ctx, pool, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2000), total)
	require.Equal(t, 100, usenet.Entitlement(100, 2000, total))
	require.Equal(t, 20, take(a, "a/3", 100, 50), "what the idle pod gave back")
}

// A grant shrinks freely and grows only into what its limits leave free.
func TestResizeShrinksFreelyAndGrowsIntoRoom(t *testing.T) {
	kv, _ := buckets(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 0)
	b := kvledger.New(kv, "pod-b", 0)
	g := usenet.Grant{Holder: "a/1", Server: "us", Host: "news.example"}
	n, err := a.Take(ctx, acct, 10, g, 8, 1)
	require.NoError(t, err)
	require.Equal(t, 8, n)

	n, err = a.Resize(ctx, acct, 10, g, 3)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	n, err = b.Take(ctx, acct, 10, usenet.Grant{Holder: "b/1", Server: "us", Host: "news.example"}, 5, 1)
	require.NoError(t, err)
	require.Equal(t, 5, n, "what the shrink freed another pod takes")

	n, err = a.Resize(ctx, acct, 10, g, 9)
	require.NoError(t, err)
	require.Equal(t, 5, n, "it grows into the five the other pod leaves, not past them")
	held, err := a.Held(ctx, acct)
	require.NoError(t, err)
	require.Equal(t, 10, held)

	// The resized grant is the one renewed: nothing is lost or doubled.
	lost, err := a.Renew(ctx)
	require.NoError(t, err)
	require.Empty(t, lost)
	held, err = a.Held(ctx, acct)
	require.NoError(t, err)
	require.Equal(t, 10, held)

	n, err = a.Resize(ctx, acct, 10, usenet.Grant{Holder: "a/none"}, 4)
	require.NoError(t, err)
	require.Zero(t, n, "no grant to resize")
}

// Every pod's bytes add to one count per account, which a ledger built
// after them reads back: a quota outlives the pod that spent it.
func TestUsageAddsAcrossPodsAndOutlivesThem(t *testing.T) {
	kv, usage := buckets(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 0).WithUsage(usage)
	b := kvledger.New(kv, "pod-b", 0).WithUsage(usage)
	u, err := a.Use(ctx, acct, usenet.QuotaNever, 1000)
	require.NoError(t, err)
	require.Equal(t, int64(1000), u.Bytes)
	u, err = b.Use(ctx, acct, usenet.QuotaNever, 500)
	require.NoError(t, err)
	require.Equal(t, int64(1500), u.Bytes)

	restarted := kvledger.New(kv, "pod-a", 0).WithUsage(usage)
	u, err = restarted.Use(ctx, acct, usenet.QuotaNever, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1500), u.Bytes, "a new pod reads what the old ones counted")

	// The manager reads the same count without the engines' client.
	read, err := usenetaccount.ReadUsage(ctx, usage, acct)
	require.NoError(t, err)
	require.Equal(t, int64(1500), read.Bytes)
	none, err := usenetaccount.ReadUsage(ctx, usage, "nobody:563:x")
	require.NoError(t, err)
	require.Zero(t, none.Bytes)
}
