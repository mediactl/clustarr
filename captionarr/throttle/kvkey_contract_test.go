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
// gives at the top of its own file: an illegal KV key has shipped twice
// before, both times because every suite that could have caught it ran only
// against the in-memory bus, which enforces no key grammar at all. This
// package mints its own key shapes (ProviderKey, TokenBucketKey) and its own
// concurrency guarantee (Acquire's token bucket), so it needs its own
// contract test against a REAL embedded NATS server -- mirroring, not
// importing, that file's helper, since pkg/events/natsbus cannot depend on
// captionarr without an import cycle back through pkg/events.
package throttle_test

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// startServer boots an embedded JetStream server with its store under the
// test's temporary directory, so the suite needs no external broker. Copied
// verbatim from pkg/events/natsbus/natsbus_test.go and
// indexarr/worker/rss/publish_test.go, which document the same duplication
// for the same reason.
func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-throttle",
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

// realKV binds to clustarr-provider-throttle on a fresh embedded JetStream
// server with the production topology applied, so the bucket's real TTL,
// storage and replica settings are in effect -- not just its name.
func realKV(t *testing.T) events.KV {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })

	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketProviderThrottle)
}

// TestKeysAreAcceptedByARealServer proves ProviderKey and TokenBucketKey
// survive Put, Get AND Delete against a real server for a hostile corpus of
// provider UIDs. The corpus is deliberately the same shape as
// pkg/events/natsbus/kvkey_contract_test.go's: a SubtitleProvider UID
// reaching this package is a Kubernetes object UID in every real cluster,
// but nothing in the Go type system enforces that, and providerUID is a
// plain string parameter all the way down.
func TestKeysAreAcceptedByARealServer(t *testing.T) {
	kv := realKV(t)
	ctx := t.Context()

	hostile := []string{
		"tt0113277:2", "Amélie", "50%", "a,b", "with space", "tmdb/949", "",
		"Wall·E (2008)", "a\tb", `quote"and'apos`, "\x00",
		".leading", "trailing.", "a..b", "abc.bucket", "already-has-.bucket-suffix",
	}

	for _, uid := range hostile {
		for name, key := range map[string]string{
			"ProviderKey":    throttle.ProviderKey(uid),
			"TokenBucketKey": throttle.TokenBucketKey(uid),
		} {
			t.Run(name+"/"+uid, func(t *testing.T) {
				require.True(t, events.ValidKVKey(key), "ValidKVKey rejected %q, which the server is about to accept", key)

				_, err := kv.Create(ctx, key, []byte("v"))
				require.NoError(t, err, "a real server rejected the key %q", key)

				ent, err := kv.Get(ctx, key)
				require.NoError(t, err, "Get rejected the key %q", key)
				require.Equal(t, []byte("v"), ent.Value)

				require.NoError(t, kv.Delete(ctx, key),
					"Delete rejected the key %q -- this is what would leave a throttle entry undeletable", key)
			})
		}
	}
}

// TestProviderKeyAndTokenBucketKeyNeverCollide is the injectivity half:
// ProviderKey(uidA) must never equal TokenBucketKey(uidB) for ANY pair, or
// the token bucket and the throttle State would alias one KV entry and
// corrupt each other. The doc comment on TokenBucketKey argues this from
// KVKeyToken's alphabet; this proves it against the corpus AND against a
// real server (so a server-side normalisation this package does not know
// about would also be caught).
func TestProviderKeyAndTokenBucketKeyNeverCollide(t *testing.T) {
	kv := realKV(t)
	ctx := t.Context()

	uids := []string{"a", "b", "abc", "abc.bucket", "a-b", "tt0113277:2", "日本語", ""}
	seen := map[string]string{}
	for _, uid := range uids {
		for _, key := range []string{throttle.ProviderKey(uid), throttle.TokenBucketKey(uid)} {
			if prev, dup := seen[key]; dup {
				t.Fatalf("key %q is produced by both %q and a previous input %q", key, uid, prev)
			}
			seen[key] = uid

			_, err := kv.Put(ctx, key, []byte(uid))
			require.NoError(t, err)
		}
	}
	// And the values written under each distinct key are still distinct on
	// read-back, which is what an actual collision would corrupt.
	for key, uid := range seen {
		ent, err := kv.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, uid, string(ent.Value), "key %q returned a different input's value", key)
	}
}

// TestAcquireEnforcesTheRateAcrossConcurrentGoroutines is the task's explicit
// ask: prove N goroutines, racing against a REAL embedded server (not the
// in-memory bus's single process-wide mutex, which would prove nothing about
// the CAS path a second worker POD actually exercises), cannot collectively
// draw tokens faster than the configured rate.
//
// capacity (burst) equals rateMilli/1000 tokens = 3 for the rate below. With
// n=9 goroutines all starting at once, the bucket can only ever satisfy 3
// "instantly" (that is what burst MEANS -- a fixed-size window check across
// the whole run would wrongly flag this legitimate burst as an overspend);
// the other 6 must be paced out at one every 1/3 second. The correct,
// standard way to check a token bucket empirically is the cumulative form:
// the k-th grant overall can never land before the bucket could physically
// have produced k tokens, i.e. before (k-capacity)/rate seconds have
// elapsed. That is asserted for every k below, not just the last one, so a
// bug that let only an EARLY grant jump the queue would still be caught.
func TestAcquireEnforcesTheRateAcrossConcurrentGoroutines(t *testing.T) {
	kv := realKV(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const (
		rateMilli = 3000 // 3 req/s, burst 3
		capacity  = rateMilli / 1000
		n         = 9
		// jitter tolerates real scheduling/network slack (goroutine wakeup,
		// loopback KV round trips) without weakening the guarantee itself:
		// it shifts every deadline slightly earlier, it does not raise the
		// rate.
		jitter = 100 * time.Millisecond
	)

	start := time.Now()
	grants := make([]time.Duration, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = throttle.Acquire(ctx, kv, "uid-concurrent", rateMilli)
			grants[i] = time.Since(start)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d", i)
	}

	sort.Slice(grants, func(i, j int) bool { return grants[i] < grants[j] })

	for i, g := range grants {
		cumulative := i + 1 // how many grants have landed at or before g
		if cumulative <= capacity {
			continue // covered by burst; no timing floor applies
		}
		minElapsed := time.Duration(cumulative-capacity) * time.Second / capacity
		require.GreaterOrEqualf(t, g+jitter, minElapsed,
			"grant #%d landed at %v, before the bucket (capacity %d, rate %d/s) could have produced it (needs >= %v)",
			cumulative, g, capacity, rateMilli/1000, minElapsed)
	}
}

// TestAcquireCombinesWithRecordErrorAgainstTheSameRealBucket is a small,
// end-to-end sanity check that the two halves of this package (the rate
// limiter and the throttle window) genuinely share one bucket without
// interfering, against the real server rather than membus.
func TestAcquireCombinesWithRecordErrorAgainstTheSameRealBucket(t *testing.T) {
	kv := realKV(t)
	ctx := t.Context()

	require.NoError(t, throttle.Acquire(ctx, kv, "uid-mixed", 5000))

	now := time.Now()
	st, err := throttle.RecordError(ctx, kv, "gestdown", "uid-mixed",
		&subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindServiceUnavailable}, now)
	require.NoError(t, err)
	require.True(t, st.Throttled(now.Add(time.Minute)))

	// Acquire still works against the SAME provider UID -- the two KV
	// entries (ProviderKey vs TokenBucketKey) are independent.
	require.NoError(t, throttle.Acquire(ctx, kv, "uid-mixed", 5000))
}
