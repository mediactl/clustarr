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
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// This file exists because the same defect has now escaped twice: a key
// builder produced a string that NATS rejects, and every suite that could
// have caught it ran against the in-memory bus, which has no key grammar at
// all. The first escape made every metadata refresh fail forever and was
// found only by the first run on a real cluster; the second left an
// ImportExclusion undeletable, because the finalizer's Delete validates the
// key exactly as the Put did.
//
// Asserting against a regex restated in a test file is one indirection away
// from the thing that actually rejects the key, and that indirection is
// where both escapes lived. So this asserts against a real NATS server.
func TestKVKeyBuildersProduceKeysARealServerAccepts(t *testing.T) {
	srv := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "clustarr-keygrammar-contract"})
	require.NoError(t, err)

	// Every value here is something a user or a provider can really put in
	// front of these builders. ImportExclusion.spec.externalIDs is an
	// unconstrained map[string]string with no pattern and no CEL, so the
	// hostile cases are reachable by anyone with kubectl.
	hostile := []string{
		"tt0113277:2",    // a colon -- illegal, and the original defect
		"Amélie",         // non-ASCII, a real film title
		"50%",            // percent
		"a,b",            // comma -- the other original separator
		"with space",     // space
		"tmdb/949",       // slash
		"",               // empty
		"Wall·E (2008)",  // parentheses and a middle dot
		"a\tb",           // a control character
		`quote"and'apos`, // quotes
	}

	for _, id := range hostile {
		for name, key := range map[string]string{
			"ExclusionKey": events.ExclusionKey("imdb", id),
			"LeaseKey":     events.LeaseKey(events.MediaKey("Movie", "default", id)),
			"PendingKey":   events.PendingKey(events.MediaKey("Series", "ns", id)),
		} {
			t.Run(name+"/"+id, func(t *testing.T) {
				// Put, Get and Delete all validate the key independently.
				// Only exercising Put would have missed the undeletable
				// ImportExclusion, whose finalizer fails on Delete.
				_, err := kv.Put(ctx, key, []byte("v"))
				require.NoError(t, err, "a real server rejected the key %q", key)

				entry, err := kv.Get(ctx, key)
				require.NoError(t, err, "Get rejected the key %q", key)
				require.Equal(t, []byte("v"), entry.Value())

				require.NoError(t, kv.Delete(ctx, key),
					"Delete rejected the key %q -- this is what leaves an object stuck in Terminating", key)
			})
		}
	}
}

// The escaping must stay injective. A sanitiser that mapped every illegal
// byte to one replacement would collapse distinct ids onto one key, and the
// bucket would then serve one item's state for another -- a quieter and
// worse failure than the rejection it was written to prevent.
func TestKVKeyBuildersDoNotCollideOnRealServer(t *testing.T) {
	srv := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "clustarr-keycollision-contract"})
	require.NoError(t, err)

	a, b := events.ExclusionKey("imdb", "a:b"), events.ExclusionKey("imdb", "a,b")
	require.NotEqual(t, a, b, "two distinct ids escaped to one key")

	_, err = kv.Put(ctx, a, []byte("first"))
	require.NoError(t, err)
	_, err = kv.Put(ctx, b, []byte("second"))
	require.NoError(t, err)

	got, err := kv.Get(ctx, a)
	require.NoError(t, err)
	require.Equal(t, []byte("first"), got.Value(), "the second id overwrote the first")
}
