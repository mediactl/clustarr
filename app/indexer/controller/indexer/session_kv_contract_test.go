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

package indexer_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// The session key is a NEW key shape in a NEW bucket, and CLAUDE.md records
// two escapes of an illegal KV key, both found only on a real server because
// every suite that could have caught them ran against the in-memory bus,
// which has no key grammar. So this runs against an embedded NATS server
// with the PRODUCTION topology applied -- the real clustarr-indexer-sessions
// bucket -- and exercises Put, Get and Delete, each of which validates the
// key independently.
func TestSessionKeysAreAcceptedByARealServer(t *testing.T) {
	bus := realBus(t)
	kv := bus.KV(events.BucketIndexerSessions)
	ctx := t.Context()

	for _, uid := range []types.UID{
		"4b0c5a7e-2f1d-4c8e-9a3b-6d7e8f901234", // what an apiserver mints
		"",                                     // a hand-built object
		"a:b/c d.e",                            // every separator nats rejects
		".leading", "trailing.", "a..b",        // the three non-regex rules
		"Amélie",
	} {
		key := indexer.SessionKey(uid)
		require.True(t, events.ValidKVKey(key), "SessionKey(%q) = %q", uid, key)
		_, err := kv.Put(ctx, key, []byte("v"))
		require.NoError(t, err, "a real server rejected SessionKey(%q) = %q", uid, key)
		e, err := kv.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, []byte("v"), e.Value)
		require.NoError(t, kv.Delete(ctx, key), "Delete rejected SessionKey(%q)", uid)
	}
	require.NotEqual(t, indexer.SessionKey("a:b"), indexer.SessionKey("a,b"), "two UIDs collapsed onto one key")
}

// The store round-trips a session through the real bucket. Client is nil, so
// this is the KV half alone: what a Load must find after a Save even when the
// Secret is unreachable.
func TestTheSessionStoreRoundTripsThroughTheRealBucket(t *testing.T) {
	bus := realBus(t)
	store := &indexer.SessionStore{KV: bus.KV(events.BucketIndexerSessions)}
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{
		Name: "t", Namespace: "media", UID: "4b0c5a7e-2f1d-4c8e-9a3b-6d7e8f901234",
	}}

	in := &cardigann.Session{
		Cookies:   []*http.Cookie{{Name: "uid", Value: "42"}},
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	require.NoError(t, store.Save(t.Context(), idx, in))
	out, err := store.Load(t.Context(), idx)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "uid=42", out.CookieHeader())
	require.True(t, in.ExpiresAt.Equal(out.ExpiresAt))
}

func realBus(t *testing.T) events.Bus {
	t.Helper()
	srv := startNATS(t) // schedule_envtest_test.go's embedded JetStream server

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus
}
