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

package limits

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func testKV(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	return bus.KV(events.BucketIndexerLimits)
}

func testIndexer(uid string, l *indexv1alpha1.Limits) *indexv1alpha1.Indexer {
	return &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr", UID: k8stypes.UID(uid)},
		Spec:       indexv1alpha1.IndexerSpec{Limits: l},
	}
}

// The escape is not optional and it is not a regex restated here:
// events.ValidKVKey is the predicate the contract test pins against a real
// embedded server.
func TestKeysGoThroughKVKeyToken(t *testing.T) {
	require.Equal(t, events.KVKeyToken("a/b:c")+".query", QueryKey("a/b:c"))
	require.Equal(t, events.KVKeyToken("a/b:c")+".grab", GrabKey("a/b:c"))
	for _, uid := range []string{"a/b:c", "", "8f14e45f-ceea-467a-9575-1c38ba1eb3bb"} {
		require.True(t, events.ValidKVKey(QueryKey(uid)))
		require.True(t, events.ValidKVKey(GrabKey(uid)))
	}
	require.NotEqual(t, QueryKey("uid"), GrabKey("uid"), "the two rings must not share a key")
}

func TestWindow(t *testing.T) {
	require.Equal(t, 24*time.Hour, Window(&indexv1alpha1.Indexer{}))
	require.Equal(t, 24*time.Hour, Window(testIndexer("u", &indexv1alpha1.Limits{})))
	require.Equal(t, time.Hour, Window(testIndexer("u", &indexv1alpha1.Limits{Unit: indexv1alpha1.LimitUnitHour})))
	require.Equal(t, 24*time.Hour, Window(testIndexer("u", &indexv1alpha1.Limits{Unit: indexv1alpha1.LimitUnitDay})))
}

func TestPrune(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Hour)
	ring := []entry{
		{At: now.Add(-3 * time.Hour).Unix()},
		{At: now.Add(-90 * time.Minute).Unix()},
		{At: now.Add(-30 * time.Minute).Unix()},
		{At: now.Unix()},
	}
	require.Equal(t, []entry{{At: now.Add(-30 * time.Minute).Unix()}, {At: now.Unix()}}, prune(ring, cutoff, 10))
	require.Equal(t, []entry{{At: now.Unix()}}, prune(ring, cutoff, 1), "the cap keeps the NEWEST entries")
	require.Empty(t, prune(nil, cutoff, 10))
}

// The query ring is a window, not a running total: the count comes down once
// the window has rolled past, so an indexer that hit its limit recovers by
// itself.
func TestReserveQueryIsAWindowNotARunningTotal(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-window", &indexv1alpha1.Limits{Unit: indexv1alpha1.LimitUnitHour})
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		r, err := ReserveQuery(ctx, kv, idx, base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		require.True(t, r.Allowed)
		require.True(t, r.Counted)
		require.Equal(t, int32(i+1), r.Count)
	}
	r, err := ReserveQuery(ctx, kv, idx, base.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int32(1), r.Count, "the earlier three aged out of the hour window")
}

// The decision this package exists for: at the limit the reservation is
// REFUSED and appends nothing, so a refusal does not itself push the window
// back, and retryAt names the moment the oldest entry leaves it.
func TestReserveQueryRefusesAtTheLimitWithoutCounting(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-limit", &indexv1alpha1.Limits{QueryLimit: ptr.To[int32](2), Unit: indexv1alpha1.LimitUnitHour})
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for i := range 2 {
		r, err := ReserveQuery(ctx, kv, idx, t0.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		require.True(t, r.Allowed)
	}
	r, err := ReserveQuery(ctx, kv, idx, t0.Add(5*time.Minute))
	require.NoError(t, err)
	require.False(t, r.Allowed, "the third query in the hour is over a limit of two")
	require.False(t, r.Counted)
	require.Equal(t, int32(2), r.Count)
	require.Equal(t, t0.Add(time.Hour+time.Second), r.RetryAt, "the oldest entry leaves the window a second after it is an hour old")

	w, err := Queries(ctx, kv, idx, t0.Add(5*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int32(2), w.Count, "a refusal appends nothing")
	require.Equal(t, r.RetryAt, w.RetryAt)

	// With no new traffic at all, the window passes and the next one is
	// allowed again -- nothing has to count for the limit to lift.
	r, err = ReserveQuery(ctx, kv, idx, r.RetryAt)
	require.NoError(t, err)
	require.True(t, r.Allowed)
	require.Equal(t, int32(2), r.Count)
}

// No limit, or Prowlarr's "0 means none", is always allowed and still
// counted: the count is observability whether or not a limit is set.
func TestReserveWithoutALimitAlwaysAllowsAndCounts(t *testing.T) {
	ctx := context.Background()
	for name, l := range map[string]*indexv1alpha1.Limits{
		"nil limits": nil,
		"nil limit":  {},
		"zero limit": {QueryLimit: ptr.To[int32](0), GrabLimit: ptr.To[int32](0)},
	} {
		t.Run(name, func(t *testing.T) {
			kv := testKV(t)
			idx := testIndexer("uid-"+name, l)
			now := time.Now()
			for i := range 3 {
				r, err := ReserveQuery(ctx, kv, idx, now)
				require.NoError(t, err)
				require.True(t, r.Allowed)
				require.Equal(t, int32(i+1), r.Count)
				require.True(t, r.RetryAt.IsZero())

				g, err := ReserveGrab(ctx, kv, idx, fmt.Sprintf("g%d", i), now)
				require.NoError(t, err)
				require.True(t, g.Allowed)
				require.Equal(t, int32(i+1), g.Count)
			}
		})
	}
}

// A grab is keyed by its GUID: the same release reserved again -- the grab
// path retried, then the download verb fetching it, then the direct-grab
// counter meeting its Download -- is one grab, and is allowed even when the
// window is full, because it already holds its slot.
func TestReserveGrabIsIdempotentAndRefusesOnlyANewGrab(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-grab", &indexv1alpha1.Limits{GrabLimit: ptr.To[int32](1)})
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	r, err := ReserveGrab(ctx, kv, idx, "a", t0)
	require.NoError(t, err)
	require.True(t, r.Allowed)
	require.True(t, r.Counted)
	require.True(t, r.Crossed(idx.Spec.Limits.GrabLimit), "the grab that fills the window crossed the limit")

	r, err = ReserveGrab(ctx, kv, idx, "a", t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, r.Allowed, "the same GUID already holds its slot")
	require.False(t, r.Counted)
	require.False(t, r.Crossed(idx.Spec.Limits.GrabLimit))

	r, err = ReserveGrab(ctx, kv, idx, "b", t0.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, r.Allowed)
	require.Equal(t, t0.Add(24*time.Hour+time.Second), r.RetryAt)

	// A direct grab already happened: it is counted, never refused.
	r, err = CountGrabAt(ctx, kv, idx, "c", t0.Add(time.Minute), t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, r.Counted)
	require.Equal(t, int32(2), r.Count)
}

// A grab counted at its OWN time: a grab older than the window is not
// counted, one inside it sits at its creation time, and is not counted twice.
func TestCountGrabAtHonoursTheGrabTime(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-at", &indexv1alpha1.Limits{Unit: indexv1alpha1.LimitUnitDay})
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	r, err := CountGrabAt(ctx, kv, idx, "old", now.Add(-25*time.Hour), now)
	require.NoError(t, err)
	require.False(t, r.Counted, "a grab older than the window is not counted")
	require.Zero(t, r.Count)

	r, err = CountGrabAt(ctx, kv, idx, "recent", now.Add(-23*time.Hour), now)
	require.NoError(t, err)
	require.True(t, r.Counted)

	r, err = CountGrabAt(ctx, kv, idx, "recent", now.Add(-23*time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, r.Counted)
	require.Equal(t, int32(1), r.Count)

	r, err = ReserveGrab(ctx, kv, idx, "later", now.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int32(1), r.Count, "the 23h-old grab has aged out of the day window")
}

// retryAt is the moment the window has room for ONE more: with the limit
// lowered below the count, more than the oldest entry has to leave.
func TestRetryAtWithTheLimitBelowTheCount(t *testing.T) {
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ring := []entry{{At: t0.Add(2 * time.Minute).Unix()}, {At: t0.Unix()}, {At: t0.Add(time.Minute).Unix()}}
	require.Equal(t, t0.Add(time.Minute+time.Hour+time.Second), retryAt(ring, ptr.To[int32](2), time.Hour),
		"three in the window against a limit of two: the second oldest must leave too")
	require.True(t, retryAt(ring, ptr.To[int32](4), time.Hour).IsZero(), "under the limit there is room now")
	require.True(t, retryAt(ring, nil, time.Hour).IsZero())
}

// Both rings keep their wire shapes, so a ring written before this package
// reads the same after it.
func TestRingsKeepTheirWireShapes(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-wire", nil)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := ReserveQuery(ctx, kv, idx, now)
	require.NoError(t, err)
	e, err := kv.Get(ctx, QueryKey("uid-wire"))
	require.NoError(t, err)
	var q []int64
	require.NoError(t, json.Unmarshal(e.Value, &q))
	require.Equal(t, []int64{now.Unix()}, q)

	_, err = ReserveGrab(ctx, kv, idx, "g", now)
	require.NoError(t, err)
	e, err = kv.Get(ctx, GrabKey("uid-wire"))
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`[{"g":"g","t":%d}]`, now.Unix()), string(e.Value))
}

func TestAnUndecodableRingIsReplaced(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-junk", nil)
	_, err := kv.Put(ctx, QueryKey("uid-junk"), []byte("{not json"))
	require.NoError(t, err)
	r, err := ReserveQuery(ctx, kv, idx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int32(1), r.Count)
}

func TestTheQueryRingSaturatesAtItsCap(t *testing.T) {
	now := time.Now()
	full := make([]entry, maxQueryEntries+50)
	for i := range full {
		full[i] = entry{At: now.Unix()}
	}
	require.Len(t, prune(full, now.Add(-time.Hour), maxQueryEntries), maxQueryEntries)
}

// A KV that always reports a revision mismatch must give up rather than spin.
func TestReserveGivesUpAfterTheCASAttempts(t *testing.T) {
	kv := &conflictKV{inner: testKV(t)}
	_, err := ReserveGrab(context.Background(), kv, testIndexer("uid-cas", nil), "g", time.Now())
	require.ErrorIs(t, err, events.ErrRevisionMismatch)
	require.Equal(t, casAttempts, kv.writes, "the loop is bounded")
}

// conflictKV answers every write with a revision mismatch, as two writers
// racing on one ring would.
type conflictKV struct {
	inner  events.KV
	writes int
}

func (k *conflictKV) Get(ctx context.Context, key string) (events.Entry, error) {
	return k.inner.Get(ctx, key)
}

func (k *conflictKV) Create(context.Context, string, []byte, ...events.KVOption) (uint64, error) {
	k.writes++
	return 0, fmt.Errorf("conflict: %w", events.ErrRevisionMismatch)
}

func (k *conflictKV) Update(context.Context, string, []byte, uint64) (uint64, error) {
	k.writes++
	return 0, fmt.Errorf("conflict: %w", events.ErrRevisionMismatch)
}

func (k *conflictKV) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	return k.inner.Put(ctx, key, val)
}

func (k *conflictKV) Delete(ctx context.Context, key string) error { return k.inner.Delete(ctx, key) }

func (k *conflictKV) DeleteRevision(ctx context.Context, key string, rev uint64) error {
	return k.inner.DeleteRevision(ctx, key, rev)
}

func (k *conflictKV) Watch(ctx context.Context, p string) (<-chan events.Entry, error) {
	return k.inner.Watch(ctx, p)
}
