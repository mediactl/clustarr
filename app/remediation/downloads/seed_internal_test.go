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

package downloads

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// countingBus counts the requests the stage makes of clustarr-transfers.
type countingBus struct {
	events.Bus
	gets, keys, entries atomic.Int32
}

func (b *countingBus) KV(bucket string) events.KV {
	kv := b.Bus.KV(bucket)
	if bucket != events.BucketTransfers {
		return kv
	}
	return &countingKV{KV: kv, bus: b}
}

type countingKV struct {
	events.KV
	bus *countingBus
}

func (k *countingKV) Get(ctx context.Context, key string) (events.Entry, error) {
	k.bus.gets.Add(1)
	return k.KV.Get(ctx, key)
}

func (k *countingKV) Keys(ctx context.Context) ([]string, error) {
	k.bus.keys.Add(1)
	return k.KV.Keys(ctx)
}

func (k *countingKV) Entries(ctx context.Context) ([]events.Entry, error) {
	k.bus.entries.Add(1)
	return k.KV.Entries(ctx)
}

// The seed every item pass waits for reads clustarr-transfers in one pass,
// not a request per record, and it starts with the loop's sources: on talos
// 2,866 requests held every worker for 165 s after a manager start, and each
// candidate parked meanwhile lapsed its budget (2026-11-26).
func TestTheUnclaimedSeedReadsTheBucketInOnePass(t *testing.T) {
	inner := membus.New(nil)
	t.Cleanup(func() { _ = inner.Close() })
	require.NoError(t, inner.Ensure(t.Context(), events.Default().ForSingleNode()))

	// The engines' own writer puts the records there.
	w := records.NewWriter(inner.KV(events.BucketTransfers), agentrecords.Transfers(), "engine-0", "v1")
	owner := schema.ItemRef{Kind: "movie", Ref: schema.Ref{Namespace: "media", Name: "heat", UID: "uid-heat"}}
	other := schema.ItemRef{Kind: "series", Ref: schema.Ref{Namespace: "media", Name: "the-wire", UID: "uid-wire"}}
	write := func(uid string, claimOwner schema.ItemRef, claimed bool, state string) {
		t.Helper()
		entry := schema.EntryRef{ID: "id-" + uid, UID: uid}
		r := &schema.TransferRecord{
			RecordHeader: schema.RecordHeader{Item: &claimOwner, Seq: 1, State: state},
			Entry:        entry,
			Claim:        &schema.TransferClaim{Owner: claimOwner, Entry: entry},
			Claimed:      claimed,
			Engine:       "usenet-0",
		}
		_, err := w.Write(t.Context(), agentrecords.TransferKey(uid), r)
		require.NoError(t, err)
	}
	const unclaimedOfOwner = 40
	for i := range unclaimedOfOwner {
		write(fmt.Sprintf("uid-unclaimed-%d", i), owner, false, schema.TransferStatePresent)
	}
	write("uid-claimed", owner, true, schema.TransferStatePresent)
	write("uid-other", other, false, schema.TransferStatePresent)

	bus := &countingBus{Bus: inner}
	s := New(Options{Bus: bus})

	// The sources start it; a pass finds it done or waits for the rest. The
	// read is waited for before the test takes the pass's place: a Do here
	// before the source's goroutine had run would be the seed itself, and
	// did so now and then (the test failed with nothing read, 2026-11-26).
	require.NoError(t, seedSource{s: s}.Start(t.Context(), nil))
	require.Eventually(t, func() bool { return bus.entries.Load() == 1 }, 10*time.Second, time.Millisecond,
		"the sources did not start the seed")
	s.seedOnce.Do(func() { t.Error("the seed had not begun") })

	assert.Len(t, s.unclaimed.forOwner(keyOfRef(owner)), unclaimedOfOwner, "the owner's unclaimed transfers, without its claimed one")
	assert.Len(t, s.unclaimed.forOwner(keyOfRef(other)), 1)
	for _, tr := range s.unclaimed.forOwner(keyOfRef(other)) {
		_, rev, ok, err := records.NewReader(inner.KV(events.BucketTransfers), agentrecords.Transfers()).Get(t.Context(), agentrecords.TransferKey("uid-other"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, rev, tr.Revision, "at the revision the record is stored at")
	}
	assert.Equal(t, int32(1), bus.entries.Load(), "one read of the bucket")
	assert.Zero(t, bus.gets.Load(), "and no request per record")
	assert.Zero(t, bus.keys.Load())
}

// A bucket the topology has not made yet holds no transfer: the seed ends
// with nothing filed.
func TestTheUnclaimedSeedOfAMissingBucketFilesNothing(t *testing.T) {
	inner := membus.New(nil)
	t.Cleanup(func() { _ = inner.Close() })
	s := New(Options{Bus: inner})
	s.seedUnclaimed(t.Context())
	assert.Empty(t, s.unclaimed.m)
}
