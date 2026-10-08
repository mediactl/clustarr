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
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// Split §6.5.2's "prev.Seq + 1, 1 when absent" restarts at 1 once the 7-day TTL
// retires a record; a task that waited longer in its queue would then outrank
// a newer request. NextSeq never goes backwards (loop spec §4.6).
func TestNextSeqIsMonotoneAcrossExpiry(t *testing.T) {
	now := time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)
	first := records.NextSeq(0, 0, now)
	assert.Equal(t, now.UnixMilli(), first, "the clock's millisecond floors a first request")
	assert.Greater(t, records.NextSeq(0, first, now), first, "the record expired; status still holds the Seq")
	assert.Greater(t, records.NextSeq(0, first, now.Add(-time.Hour)), first, "a clock stepped back never reissues a Seq")
	assert.Greater(t, records.NextSeq(0, 0, now.Add(8*24*time.Hour)), first, "both expired: the floor still moves on")
	assert.EqualValues(t, 8, records.NextSeq(7, 3, time.UnixMilli(5)))
	assert.EqualValues(t, 8, records.NextSeq(3, 7, time.UnixMilli(5)), "status ahead of the record")
	assert.Equal(t, 7*24*time.Hour, records.FactWindow(6*24*time.Hour, 2*24*time.Hour), "never past the bucket's TTL")
	assert.Equal(t, 3*time.Hour, records.FactWindow(2*time.Hour, time.Hour))
	assert.Less(t, records.RepublishWindow, time.Hour, "inside the work streams' one-hour Duplicates window")
}

func TestOversizedRecordIsRefused(t *testing.T) {
	bus := newBus(t)
	q := records.NewRequester(bus.KV(bucket), spec())
	big := at(1, "")
	big.Inputs = strings.Repeat("x", 5<<10)
	_, err := q.Request(t.Context(), key, 0, big)
	require.ErrorIs(t, err, records.ErrTooLarge)
	assert.False(t, read(t, bus).ok, "nothing was written")

	a := records.NewAnswerer(bus.KV(bucket), spec(), "worker-0", "test")
	ans := at(1, records.StateAnswered)
	ans.Result = strings.Repeat("y", 5<<10)
	_, err = a.Answer(t.Context(), key, ans)
	require.ErrorIs(t, err, records.ErrTooLarge)
	assert.False(t, read(t, bus).ok, "nothing was written")

	// A failure is clipped to 1024 bytes on a rune boundary, so a failed
	// answer always fits (§4.5 "Writers clamp first").
	failed := at(1, records.StateFailed)
	failed.Failure = strings.Repeat("é", 2000)
	v, err := a.Answer(t.Context(), key, failed)
	require.NoError(t, err)
	assert.Equal(t, records.Wrote, v)
	got := read(t, bus).rec.Failure
	assert.Equal(t, records.MaxFailure, schema.MaxRecordFailure)
	assert.LessOrEqual(t, len(got), records.MaxFailure)
	assert.Greater(t, len(got), records.MaxFailure-utf8.UTFMax, "clipped, not dropped")
	assert.True(t, utf8.ValidString(got))
}

// Records are input to the loop, never instructions: an undecodable value, a
// value of another remediation's schema, and another file's record under this
// key all read as absent, at their revision, and each counts a decode error.
func TestUndecodableRecordReadsAbsent(t *testing.T) {
	mustJSON := func(r *rec) []byte {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		return b
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"not JSON", []byte("{not json")},
		{"another remediation's schema", mustJSON(&rec{RecordHeader: schema.RecordHeader{
			Schema: "records.other.v1", MediaFile: file, Seq: 1, State: records.StateRequested,
		}})},
		{"another file's record under this key", mustJSON(&rec{RecordHeader: schema.RecordHeader{
			Schema: testSchema, MediaFile: schema.Ref{Namespace: "media", Name: "other", UID: "uid-other"}, Seq: 1, State: records.StateRequested,
		}})},
		{"another sub-key's record under this key", mustJSON(&rec{RecordHeader: schema.RecordHeader{
			Schema: testSchema, MediaFile: file, Sub: "en", Seq: 1, State: records.StateRequested,
		}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := newBus(t)
			rev, err := bus.KV(bucket).Put(t.Context(), key, tc.raw)
			require.NoError(t, err)
			var counted []string
			sp := spec()
			sp.Errors = func(op string) { counted = append(counted, op) }
			q := records.NewRequester(bus.KV(bucket), sp)
			r, gotRev, ok, err := q.Get(t.Context(), key)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Nil(t, r)
			assert.Equal(t, rev, gotRev, "a write replaces it at its revision")
			assert.Equal(t, []string{"decode"}, counted)
			_, err = q.Request(t.Context(), key, gotRev, at(1, ""))
			require.NoError(t, err)
			assert.True(t, read(t, bus).ok)
		})
	}
}
