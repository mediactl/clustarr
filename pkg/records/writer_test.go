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

package records_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

var (
	owner = schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "heat-1995", UID: "uid-movie"}}
	entry = schema.EntryRef{ID: "e1", UID: "uid-entry-1"}
)

// transfer is a transfer record as the engine renders one: keyed by its
// entry's UID, naming its owner as the item and no MediaFile.
func transfer(seq, downloaded int64) *schema.TransferRecord {
	return &schema.TransferRecord{
		RecordHeader:    schema.RecordHeader{Item: &owner, Seq: seq, State: schema.TransferStatePresent},
		Entry:           entry,
		Engine:          "client-0",
		DownloadedBytes: downloaded,
	}
}

// TestKeyOfGovernsTheKeyCheck (ADR-0019 §4.3): an agent-written bucket keys
// by something other than the MediaFile UID, and Spec.KeyOf is what the key
// is checked against on read and on write. A transfer record keyed by its
// entry UID reads present; under another key it reads absent and counts
// decode, and a writer refuses to put it there. Without KeyOf the same
// record names no MediaFile, so it reads absent under its own key.
func TestKeyOfGovernsTheKeyCheck(t *testing.T) {
	bus := newBus(t)
	kv := bus.KV(events.BucketTransfers)
	var counted []string
	sp := agentrecords.Transfers()
	sp.Errors = func(op string) { counted = append(counted, op) }
	w := records.NewWriter(kv, sp, "engine-0", "v-test")
	r := records.NewReader(kv, sp)
	k := agentrecords.TransferKey(entry.UID)

	v, err := w.Write(t.Context(), k, transfer(1, 10))
	require.NoError(t, err)
	require.Equal(t, records.Wrote, v)
	got, rev, ok, err := r.Get(t.Context(), k)
	require.NoError(t, err)
	require.True(t, ok, "keyed by its entry UID, it reads present")
	assert.NotZero(t, rev)
	assert.Equal(t, entry, got.Entry)
	assert.Equal(t, schema.TransferRecord{}.Schema(), got.RecordHeader.Schema, "the writer stamps the schema")
	assert.Empty(t, counted)

	noKeyOf := agentrecords.Transfers()
	noKeyOf.KeyOf = nil
	noKeyOf.Errors = sp.Errors
	_, _, ok, err = records.NewReader(kv, noKeyOf).Get(t.Context(), k)
	require.NoError(t, err)
	assert.False(t, ok, "without KeyOf the key is checked against the empty MediaFile UID")
	assert.Equal(t, []string{"decode"}, counted)

	other := agentrecords.TransferKey("uid-entry-2")
	_, err = w.Write(t.Context(), other, transfer(1, 10))
	require.Error(t, err, "a writer refuses a record under a key its KeyOf does not name")
	_, err = kv.Get(t.Context(), other)
	require.ErrorIs(t, err, events.ErrKeyNotFound, "nothing was written")

	// Another writer put this entry's record under the other entry's key.
	b, err := json.Marshal(got)
	require.NoError(t, err)
	putRev, err := kv.Put(t.Context(), other, b)
	require.NoError(t, err)
	counted = nil
	_, gotRev, ok, err := r.Get(t.Context(), other)
	require.NoError(t, err)
	assert.False(t, ok, "under another key it reads absent")
	assert.Equal(t, putRev, gotRev, "at its revision, so a write replaces it")
	assert.Equal(t, []string{"decode"}, counted)

	// A sub-keyed bucket checks the sub-key too: an entry's inspect record is
	// not its execute record.
	imports := records.NewWriter(bus.KV(events.BucketImports), agentrecords.Imports(), "import-0", "v-test")
	inspect := &schema.ImportRecord{
		RecordHeader: schema.RecordHeader{Item: &owner, Sub: schema.ImportSubInspect, Seq: 1, State: records.StateAnswered},
		Entry:        entry,
	}
	v, err = imports.Write(t.Context(), agentrecords.ImportKey(entry.UID, schema.ImportSubInspect), inspect)
	require.NoError(t, err)
	assert.Equal(t, records.Wrote, v)
	_, err = imports.Write(t.Context(), agentrecords.ImportKey(entry.UID, schema.ImportSubExecute), inspect)
	require.Error(t, err, "an inspect record under the execute key")
	_, err = imports.Write(t.Context(), agentrecords.TransferKey(entry.UID), inspect)
	require.Error(t, err, "an inspect record under the entry's bare key")
}

// TestWriterDropsALowerSeqAndAllowsARepeatedOne (ADR-0019 §4.3, ruling R6):
// the one writer of a key drops a record whose Seq is below the stored one
// and rewrites at a repeated Seq -- a transfer rewrites its counters at one
// command's Seq. It stamps the writer and the schema, and clips a failure.
func TestWriterDropsALowerSeqAndAllowsARepeatedOne(t *testing.T) {
	bus := newBus(t)
	kv := bus.KV(events.BucketTransfers)
	w := records.NewWriter(kv, agentrecords.Transfers(), "engine-0", "v-test")
	r := records.NewReader(kv, agentrecords.Transfers())
	k := agentrecords.TransferKey(entry.UID)
	stored := func() *schema.TransferRecord {
		t.Helper()
		got, _, ok, err := r.Get(t.Context(), k)
		require.NoError(t, err)
		require.True(t, ok)
		return got
	}

	for _, step := range []struct {
		name            string
		seq, downloaded int64
		want            records.Verdict
		wantSeq, wantDL int64
	}{
		{"the first write", 5, 100, records.Wrote, 5, 100},
		{"a repeated Seq rewrites the counters", 5, 200, records.Wrote, 5, 200},
		{"a lower Seq is dropped", 4, 300, records.Dropped, 5, 200},
		{"a higher Seq is written", 6, 50, records.Wrote, 6, 50},
		{"the old command's Seq is dropped now", 5, 999, records.Dropped, 6, 50},
	} {
		v, err := w.Write(t.Context(), k, transfer(step.seq, step.downloaded))
		require.NoError(t, err, step.name)
		assert.Equal(t, step.want, v, step.name)
		got := stored()
		assert.Equal(t, step.wantSeq, got.Seq, step.name)
		assert.Equal(t, step.wantDL, got.DownloadedBytes, step.name)
		assert.Equal(t, "engine-0", got.Writer, step.name)
		assert.Equal(t, "v-test", got.WriterVersion, step.name)
	}

	// Update's mutate returning a lower Seq is dropped too.
	v, err := w.Update(t.Context(), k, func(cur *schema.TransferRecord, ok bool) (*schema.TransferRecord, bool) {
		require.True(t, ok)
		return transfer(cur.Seq-1, 1), true
	})
	require.NoError(t, err)
	assert.Equal(t, records.Dropped, v)
	assert.EqualValues(t, 50, stored().DownloadedBytes)

	// A failure is clipped on a rune boundary.
	failed := transfer(7, 50)
	failed.State, failed.Failure = schema.TransferStateFailed, strings.Repeat("é", 2000)
	v, err = w.Write(t.Context(), k, failed)
	require.NoError(t, err)
	require.Equal(t, records.Wrote, v)
	got := stored().Failure
	assert.LessOrEqual(t, len(got), records.MaxFailure)
	assert.True(t, utf8.ValidString(got))

	// A record over the bucket's value cap is refused, and nothing changes.
	small := records.NewWriter(bus.KV(bucket), spec(), "w", "v")
	big := at(1, records.StateAnswered)
	big.Result = strings.Repeat("x", 5<<10)
	_, err = small.Write(t.Context(), key, big)
	require.ErrorIs(t, err, records.ErrTooLarge)
	assert.False(t, read(t, bus).ok)
}

// TestWriterRereadsAfterACASMiss: a second writer lands between the
// writer's read and its compare-and-swap. The writer reads again and decides
// on the newer record: a stale Seq is dropped, and Update's read-modify-write
// -- the index agent's fan-out and RSS poll share one (ADR-0019 §7.5) --
// applies its change on top of the other's rather than losing it.
func TestWriterRereadsAfterACASMiss(t *testing.T) {
	t.Run("a newer Seq landed: dropped", func(t *testing.T) {
		bus := newBus(t)
		seed(t, bus, at(3, records.StateAnswered))
		other := records.NewWriter(bus.KV(bucket), spec(), "other", "v-test")
		kv := &interleavedKV{KV: bus.KV(bucket)}
		kv.after = func() {
			v, err := other.Write(context.Background(), key, at(6, records.StateAnswered))
			require.NoError(t, err)
			require.Equal(t, records.Wrote, v)
		}
		w := records.NewWriter[*rec](kv, spec(), "worker-0", "v-test")
		v, err := w.Write(t.Context(), key, at(5, records.StateAnswered))
		require.NoError(t, err)
		assert.Equal(t, records.Dropped, v)
		got := read(t, bus)
		assert.EqualValues(t, 6, got.rec.Seq)
		assert.Equal(t, "other", got.rec.Writer)
		assert.EqualValues(t, 2, kv.gets.Load(), "Write read again after its compare-and-swap missed")
	})

	t.Run("a concurrent update: both land", func(t *testing.T) {
		bus := newBus(t)
		base := at(3, records.StateAnswered)
		base.Count = 1
		seed(t, bus, base)
		increment := func(cur *rec, ok bool) (*rec, bool) {
			require.True(t, ok)
			cur.Count++
			return cur, true
		}
		other := records.NewWriter(bus.KV(bucket), spec(), "rss", "v-test")
		kv := &interleavedKV{KV: bus.KV(bucket)}
		kv.after = func() {
			v, err := other.Update(context.Background(), key, increment)
			require.NoError(t, err)
			require.Equal(t, records.Wrote, v)
		}
		w := records.NewWriter[*rec](kv, spec(), "fanout", "v-test")
		v, err := w.Update(t.Context(), key, increment)
		require.NoError(t, err)
		assert.Equal(t, records.Wrote, v)
		got := read(t, bus)
		assert.Equal(t, 3, got.rec.Count, "neither increment was lost")
		assert.Equal(t, "fanout", got.rec.Writer)
		assert.EqualValues(t, 2, kv.gets.Load())
	})
}
