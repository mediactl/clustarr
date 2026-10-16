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

package kvledger

import (
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// A Give that lands while Renew is mid-swap is not undone by the swap's
// retry: Renew re-checks what the pod still holds before re-adding a
// grant (review I8).
func TestRenewDoesNotResurrectAGrantGivenBackMeanwhile(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "js")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
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
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketUsenetConnections)
	ctx := t.Context()
	const acct = "news.example:563:me"

	l := New(kv, "pod-a", 0)
	n, err := l.Take(ctx, acct, 10, usenet.Grant{Holder: "eng/dl", Server: "x"}, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 10, n)

	armed, inHook := true, false
	l.now = func() time.Time {
		if armed && !inHook {
			armed, inHook = false, true
			// The job gives its grant back while Renew is mid-swap.
			require.NoError(t, l.Give(ctx, acct, "eng/dl"))
			inHook = false
		}
		return time.Now()
	}
	lost, err := l.Renew(ctx)
	require.NoError(t, err)
	require.Empty(t, lost)
	held, err := l.Held(ctx, acct)
	require.NoError(t, err)
	require.Zero(t, held, "a given-back grant was re-added by a concurrent Renew")
}
