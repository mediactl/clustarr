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

package download

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func newTestKV(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	return bus.KV(events.BucketIndexerLimits)
}

func testIndexer(ns, name, uid string, unit indexv1alpha1.LimitUnit) *indexv1alpha1.Indexer {
	return &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: k8stypes.UID(uid)},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://tr.example",
			Limits:  &indexv1alpha1.Limits{Unit: unit},
		},
	}
}

func TestCountGrabIsIdempotentUnderRedelivery(t *testing.T) {
	ctx := context.Background()
	kv := newTestKV(t)
	idx := testIndexer("media", "nzbgeek", "uid-1", indexv1alpha1.LimitUnitDay)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	n, counted, err := CountGrab(ctx, kv, idx, "guid-a", now)
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(1), n)

	// The SAME grab again -- an RPC retry, a duplicate delivery, a grabarr
	// requeue. It must not double count.
	n, counted, err = CountGrab(ctx, kv, idx, "guid-a", now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, counted, "a redelivered grab is not a new grab")
	require.Equal(t, int32(1), n)

	n, counted, err = CountGrab(ctx, kv, idx, "guid-b", now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(2), n)
}

// A nil spec.limits still counts. The counter is observability whether or not
// a limit is configured, and the CRD's default unit is day.
func TestCountGrabCountsWithNoLimitsConfigured(t *testing.T) {
	ctx := context.Background()
	kv := newTestKV(t)
	idx := testIndexer("media", "tr", "uid-nolimits", indexv1alpha1.LimitUnitDay)
	idx.Spec.Limits = nil
	require.Equal(t, 24*time.Hour, grabWindow(idx))

	n, counted, err := CountGrab(ctx, kv, idx, "g", time.Now())
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(1), n)
}

func TestGrabRingPrunesOutsideTheWindow(t *testing.T) {
	ctx := context.Background()
	kv := newTestKV(t)
	idx := testIndexer("media", "tr", "uid-2", indexv1alpha1.LimitUnitHour)
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, _, err := CountGrab(ctx, kv, idx, "old", t0)
	require.NoError(t, err)
	n, counted, err := CountGrab(ctx, kv, idx, "new", t0.Add(90*time.Minute))
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(1), n, "the hour-old grab aged out of a 1h window")

	// And the aged-out GUID is countable again: it is no longer a redelivery.
	n, counted, err = CountGrab(ctx, kv, idx, "old", t0.Add(91*time.Minute))
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(2), n)
}

func TestGrabRingIsCapped(t *testing.T) {
	ring := make([]grabEntry, 0, maxRingEntries+10)
	now := time.Now()
	for i := range maxRingEntries + 10 {
		ring = append(ring, grabEntry{GUID: fmt.Sprintf("g%d", i), At: now.Unix()})
	}
	got := pruneRing(ring, now.Add(-time.Hour), maxRingEntries)
	require.Len(t, got, maxRingEntries)
	require.Equal(t, "g10", got[0].GUID, "the OLDEST entries are the ones dropped")
}

func TestPruneRingDropsWhatIsOutsideTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ring := []grabEntry{
		{GUID: "ancient", At: now.Add(-48 * time.Hour).Unix()},
		{GUID: "edge", At: now.Add(-time.Hour).Unix()},
		{GUID: "fresh", At: now.Unix()},
	}
	got := pruneRing(ring, now.Add(-time.Hour), maxRingEntries)
	require.Len(t, got, 2, "an entry exactly at the cutoff is inside the window")
	require.Equal(t, "edge", got[0].GUID)
	require.Equal(t, "fresh", got[1].GUID)

	require.Empty(t, pruneRing(nil, now, maxRingEntries))
}

func TestGrabRingKeyGoesThroughKVKeyToken(t *testing.T) {
	// A UID is a UUID today, but the key builder must not assume it: every KV
	// key in this repo goes through KVKeyToken, and an illegal key makes both
	// Put and Delete fail.
	require.Equal(t, events.KVKeyToken("a/b:c")+".grab", GrabRingKey("a/b:c"))
	require.True(t, events.ValidKVKey(GrabRingKey("a/b:c")))
	require.True(t, events.ValidKVKey(GrabRingKey("")))
	require.True(t, events.ValidKVKey(GrabRingKey("8f14e45f-ceea-467a-9575-1c38ba1eb3bb")))
}

// A value we cannot decode is a value we replace: failing shut would wedge
// counting for this indexer for the bucket's whole 2d TTL.
func TestCountGrabReplacesAnUndecodableRing(t *testing.T) {
	ctx := context.Background()
	kv := newTestKV(t)
	idx := testIndexer("media", "tr", "uid-5", indexv1alpha1.LimitUnitDay)
	_, err := kv.Put(ctx, GrabRingKey(string(idx.UID)), []byte("{not json"))
	require.NoError(t, err)

	n, counted, err := CountGrab(ctx, kv, idx, "guid-a", time.Now())
	require.NoError(t, err)
	require.True(t, counted)
	require.Equal(t, int32(1), n)
}

// The key comes from the LIVE object's UID. A caller-supplied stale UID would
// open a second ring for one indexer, and the count would restart at 1.
func TestCountGrabKeysOnTheObjectsOwnUID(t *testing.T) {
	ctx := context.Background()
	kv := newTestKV(t)
	a := testIndexer("media", "tr", "uid-live", indexv1alpha1.LimitUnitDay)
	b := testIndexer("media", "tr", "uid-other", indexv1alpha1.LimitUnitDay)
	now := time.Now()

	_, _, err := CountGrab(ctx, kv, a, "g", now)
	require.NoError(t, err)
	n, _, err := CountGrab(ctx, kv, b, "g", now)
	require.NoError(t, err)
	require.Equal(t, int32(1), n, "a different UID is a different ring")

	_, err = kv.Get(ctx, GrabRingKey("uid-live"))
	require.NoError(t, err)
}

// A KV that always reports a revision mismatch must give up rather than spin.
func TestCountGrabGivesUpAfterTheCASAttempts(t *testing.T) {
	kv := &conflictKV{inner: newTestKV(t)}
	idx := testIndexer("media", "tr", "uid-6", indexv1alpha1.LimitUnitDay)
	_, _, err := CountGrab(context.Background(), kv, idx, "g", time.Now())
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

func (k *conflictKV) Create(
	_ context.Context, _ string, _ []byte, _ ...events.KVOption,
) (uint64, error) {
	k.writes++
	return 0, fmt.Errorf("conflict: %w", events.ErrRevisionMismatch)
}

func (k *conflictKV) Update(_ context.Context, _ string, _ []byte, _ uint64) (uint64, error) {
	k.writes++
	return 0, fmt.Errorf("conflict: %w", events.ErrRevisionMismatch)
}

func (k *conflictKV) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	return k.inner.Put(ctx, key, val)
}

func (k *conflictKV) Delete(ctx context.Context, key string) error {
	return k.inner.Delete(ctx, key)
}

func (k *conflictKV) Watch(ctx context.Context, p string) (<-chan events.Entry, error) {
	return k.inner.Watch(ctx, p)
}
