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

package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// The escape is not optional and it is not a regex restated here: a NATS KV
// key must satisfy nats.go's character set PLUS no leading ".", no trailing
// "." and no "..". events.ValidKVKey is the predicate the contract test pins
// against a real embedded server, so this asserts through it rather than
// restating the grammar -- restating it is how ValidKVKey itself shipped
// permissive.
func TestQueryRingKeyGoesThroughKVKeyToken(t *testing.T) {
	require.Equal(t, events.KVKeyToken("a/b:c")+".query", QueryRingKey("a/b:c"))
	require.True(t, events.ValidKVKey(QueryRingKey("a/b:c")))
	require.True(t, events.ValidKVKey(QueryRingKey("")))
	require.True(t, events.ValidKVKey(QueryRingKey("8f14e45f-ceea-467a-9575-1c38ba1eb3bb")))
	require.NotEqual(t, QueryRingKey("uid"), GrabRingKeyLike("uid"),
		"the query ring and the grab ring must not share a key")
}

// GrabRingKeyLike mirrors app/indexer/download's key shape without importing a
// sibling verb: the point of the assertion above is that the two suffixes
// differ, not that this reimplements anything.
func GrabRingKeyLike(uid string) string { return events.KVKeyToken(uid) + ".grab" }

func TestQueryWindow(t *testing.T) {
	require.Equal(t, 24*time.Hour, queryWindow(&indexv1alpha1.Indexer{}))
	require.Equal(t, 24*time.Hour, queryWindow(&indexv1alpha1.Indexer{
		Spec: indexv1alpha1.IndexerSpec{Limits: &indexv1alpha1.Limits{}},
	}))
	require.Equal(t, time.Hour, queryWindow(&indexv1alpha1.Indexer{
		Spec: indexv1alpha1.IndexerSpec{Limits: &indexv1alpha1.Limits{
			Unit: indexv1alpha1.LimitUnitHour,
		}},
	}))
	require.Equal(t, 24*time.Hour, queryWindow(&indexv1alpha1.Indexer{
		Spec: indexv1alpha1.IndexerSpec{Limits: &indexv1alpha1.Limits{
			Unit: indexv1alpha1.LimitUnitDay,
		}},
	}))
}

func TestPruneQueryRing(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Hour)
	ring := []int64{
		now.Add(-3 * time.Hour).Unix(),
		now.Add(-90 * time.Minute).Unix(),
		now.Add(-30 * time.Minute).Unix(),
		now.Unix(),
	}
	require.Equal(t, []int64{now.Add(-30 * time.Minute).Unix(), now.Unix()},
		pruneQueryRing(ring, cutoff, 10))

	// The cap keeps the NEWEST entries.
	require.Equal(t, []int64{now.Unix()}, pruneQueryRing(ring, cutoff, 1))
	require.Empty(t, pruneQueryRing(nil, cutoff, 10))
}

func testIndexer(uid string) *indexv1alpha1.Indexer {
	return &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr", UID: types.UID(uid)},
	}
}

func testKV(t *testing.T) events.KV {
	t.Helper()
	ctx := context.Background()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	return bus.KV(events.BucketIndexerLimits)
}

// The whole reason the ring exists: the count comes DOWN again once the
// window has rolled past, so an indexer that hit its query limit recovers by
// itself. A status counter with no window never does, and the Indexer
// reconciler's RateLimited condition would latch with it.
func TestCountQueryIsAWindowNotARunningTotal(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-window")
	idx.Spec.Limits = &indexv1alpha1.Limits{Unit: indexv1alpha1.LimitUnitHour}

	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		n, err := countQuery(ctx, kv, idx, base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		require.Equal(t, int32(i+1), n)
	}

	// Two hours later every one of those is outside the 1h window.
	n, err := countQuery(ctx, kv, idx, base.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int32(1), n, "the window did not roll")
}

// A search RPC is request/reply, so a repeated search really is a repeated
// query against the indexer. Unlike the grab ring there is no idempotency
// key and repeats must count.
func TestCountQueryCountsEveryQuery(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-repeat")
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	a, err := countQuery(ctx, kv, idx, now)
	require.NoError(t, err)
	b, err := countQuery(ctx, kv, idx, now)
	require.NoError(t, err)
	require.Equal(t, int32(1), a)
	require.Equal(t, int32(2), b)
}

// A value we cannot decode is a value we replace: failing shut would wedge
// counting for this indexer for the bucket's whole 2d TTL.
func TestCountQueryReplacesAnUndecodableRing(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-garbage")
	_, err := kv.Put(ctx, QueryRingKey(string(idx.UID)), []byte("{not json"))
	require.NoError(t, err)

	n, err := countQuery(ctx, kv, idx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int32(1), n)
}

func TestCountQuerySaturatesAtTheRingCap(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	full := make([]int64, maxQueryRingEntries+50)
	for i := range full {
		full[i] = now.Unix()
	}
	require.Len(t, pruneQueryRing(full, now.Add(-time.Hour), maxQueryRingEntries),
		maxQueryRingEntries)
}

// A nil Bus disables accounting rather than failing the search, and a nil
// count means "leave status.queriesInWindow exactly as it is".
func TestServiceCountQueryWithoutABus(t *testing.T) {
	s := &Service{}
	require.Nil(t, s.countQuery(context.Background(), testIndexer("uid-nobus")))
}

func TestServiceCountQueryReturnsTheWindowCount(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	s := &Service{Bus: bus, Now: time.Now}
	got := s.countQuery(ctx, testIndexer("uid-service"))
	require.Equal(t, ptr.To(int32(1)), got)
}
