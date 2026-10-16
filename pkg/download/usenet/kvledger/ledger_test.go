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

// The ledger's contract against a real embedded NATS server, with the
// production topology applied: the in-memory bus has no key grammar and no
// revision semantics worth proving against.
package kvledger_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/download/usenet/kvledger"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-kvledger",
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

func bucket(t *testing.T) events.KV {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketUsenetConnections)
}

const acct = "news.example:563:me@x"

// Two pods on one account never hold more than the limit together, however
// their takes interleave: every write is a compare-and-swap on the entry.
func TestTwoPodsNeverExceedTheAccountLimit(t *testing.T) {
	kv := bucket(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 0)
	b := kvledger.New(kv, "pod-b", 0)

	var (
		wg   sync.WaitGroup
		held atomic.Int64
		peak atomic.Int64
	)
	for i := range 20 {
		l, pod := a, "pod-a"
		if i%2 == 1 {
			l, pod = b, "pod-b"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			holder := pod + "/dl-" + string(rune('a'+i))
			n, err := l.Take(ctx, acct, 10, usenet.Grant{Holder: holder, Server: "x"}, 3, 1)
			require.NoError(t, err)
			if n == 0 {
				return
			}
			cur := held.Add(int64(n))
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			held.Add(-int64(n))
			require.NoError(t, l.Give(ctx, acct, holder))
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, peak.Load(), int64(10), "the pods held more than the limit together")
	n, err := a.Held(ctx, acct)
	require.NoError(t, err)
	require.Zero(t, n, "every grant was given back")
}

// A grant past its expiry counts for nothing: a pod that died frees its
// connections without anyone cleaning up.
func TestAnExpiredGrantFreesItsConnections(t *testing.T) {
	kv := bucket(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 50*time.Millisecond)
	b := kvledger.New(kv, "pod-b", 50*time.Millisecond)

	n, err := a.Take(ctx, acct, 10, usenet.Grant{Holder: "pod-a/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	n, err = b.Take(ctx, acct, 10, usenet.Grant{Holder: "pod-b/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Zero(t, n, "the account is full")

	time.Sleep(60 * time.Millisecond)
	n, err = b.Take(ctx, acct, 10, usenet.Grant{Holder: "pod-b/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 10, n, "pod-a's grant expired")
}

// Renew keeps a live grant alive, and reports one it found gone when the
// account has no room to take it back: the transfer must take a new one.
func TestRenewKeepsAGrantAliveAndReportsOneItLost(t *testing.T) {
	kv := bucket(t)
	ctx := t.Context()
	a := kvledger.New(kv, "pod-a", 80*time.Millisecond)
	b := kvledger.New(kv, "pod-b", 80*time.Millisecond)

	n, err := a.Take(ctx, acct, 10, usenet.Grant{Holder: "pod-a/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	for range 4 {
		time.Sleep(40 * time.Millisecond)
		lost, err := a.Renew(ctx)
		require.NoError(t, err)
		require.Empty(t, lost)
	}
	n, err = b.Held(ctx, acct)
	require.NoError(t, err)
	require.Equal(t, 10, n, "renewed grants stay live past the TTL")

	time.Sleep(100 * time.Millisecond) // a missed renewal: pod-a's grant expires
	n, err = b.Take(ctx, acct, 10, usenet.Grant{Holder: "pod-b/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	lost, err := a.Renew(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"pod-a/dl"}, lost)
	n, err = a.Held(ctx, acct)
	require.NoError(t, err)
	require.Equal(t, 10, n, "the lost grant was not re-added over pod-b's")
}

// An account string carries ':' and '@', and the key must survive a real
// server's grammar on every operation.
func TestTheAccountKeyIsValidOnARealServer(t *testing.T) {
	kv := bucket(t)
	ctx := t.Context()
	l := kvledger.New(kv, "pod", 0)
	for _, account := range []string{acct, "", "a..b", ".x", "x.", "with space:119:ü"} {
		key := events.UsenetAccountKey(account)
		require.True(t, events.ValidKVKey(key), "ValidKVKey rejected %q", key)
		n, err := l.Take(ctx, account, 2, usenet.Grant{Holder: "h", Server: "x"}, 1, 1)
		require.NoError(t, err, "account %q", account)
		require.Equal(t, 1, n)
		require.NoError(t, l.Give(ctx, account, "h"))
	}
	_, err := kv.Get(context.Background(), events.UsenetAccountKey(acct))
	require.NoError(t, err, "the entry stays, empty, after its last grant is given back")
}
