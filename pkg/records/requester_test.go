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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/records"
)

func TestRequestIsACompareAndSwapAtTheReadRevision(t *testing.T) {
	bus := newBus(t)
	q := records.NewRequester(bus.KV(bucket), spec())
	rev, err := q.Request(t.Context(), key, 0, at(1, ""))
	require.NoError(t, err)
	got := read(t, bus)
	require.True(t, got.ok)
	assert.Equal(t, rev, got.rev)
	assert.Equal(t, records.StateRequested, got.rec.State)
	assert.False(t, got.rec.RequestedAt.IsZero())
	assert.Equal(t, testSchema, got.rec.Schema)

	_, err = q.Request(t.Context(), key, 0, at(2, ""))
	require.ErrorIs(t, err, records.ErrRaced, "a request from a stale read must lose")
	_, err = q.Request(t.Context(), key, rev+1, at(2, ""))
	require.ErrorIs(t, err, records.ErrRaced, "a revision the record never had must lose")
	_, err = q.Request(t.Context(), key, rev, at(1, ""))
	require.Error(t, err, "a Seq not above the record's is refused")
	assert.NotErrorIs(t, err, records.ErrRaced)
	assert.EqualValues(t, 1, read(t, bus).rec.Seq, "no losing request wrote")

	// A request clears what a worker wrote for the previous Seq.
	a := records.NewAnswerer(bus.KV(bucket), spec(), "worker-0", "v-test")
	failed := at(1, records.StateFailed)
	failed.Failure, failed.Transient = "boom", true
	v, err := a.Answer(t.Context(), key, failed)
	require.NoError(t, err)
	require.Equal(t, records.Wrote, v)
	rev = read(t, bus).rev
	_, err = q.Request(t.Context(), key, rev, at(2, ""))
	require.NoError(t, err)
	after := read(t, bus).rec
	assert.EqualValues(t, 2, after.Seq)
	assert.Equal(t, records.StateRequested, after.State)
	assert.Empty(t, after.Writer)
	assert.Empty(t, after.Failure)
	assert.False(t, after.Transient)
	assert.Nil(t, after.AnsweredAt)

	// A worker's write landing between Request's own read and its write
	// loses Request the swap: the write itself is the compare-and-swap.
	rev = read(t, bus).rev
	kv := &interleavedKV{KV: bus.KV(bucket)}
	kv.after = func() {
		v, err := records.NewAnswerer(bus.KV(bucket), spec(), "worker-1", "v-test").Claim(context.Background(), key, 2, false)
		require.NoError(t, err)
		require.Equal(t, records.Wrote, v)
	}
	_, err = records.NewRequester[*rec](kv, spec()).Request(t.Context(), key, rev, at(3, ""))
	require.ErrorIs(t, err, records.ErrRaced)
	after = read(t, bus).rec
	assert.EqualValues(t, 2, after.Seq)
	assert.Equal(t, records.StateClaimed, after.State, "the worker's claim stands")
}

// TestWithdrawRules is loop spec §4.7's table for Withdraw(seq 4).
func TestWithdrawRules(t *testing.T) {
	const S = 4
	fact := func(seq int64) *rec { r := at(seq, records.StateAnswered); r.Swapped = true; return r }
	for _, tc := range []struct {
		name  string
		cur   *rec
		opts  []records.Option
		wrote bool
		err   error
	}{
		{"absent", nil, nil, true, nil},
		{"requested below", at(3, records.StateRequested), nil, true, nil},
		{"answered below", at(3, records.StateAnswered), nil, true, nil},
		{"an unincorporated fact below", fact(3), nil, false, records.ErrUnincorporatedFact},
		{"an incorporated fact below", fact(3), []records.Option{records.IncorporatedThrough(3)}, true, nil},
		{"requested at S", at(S, records.StateRequested), nil, true, nil},
		{"claimed at S", at(S, records.StateClaimed), nil, true, nil},
		{"deferred at S", at(S, records.StateDeferred), nil, true, nil},
		{"withdrawn at S: done", at(S, records.StateWithdrawn), nil, false, nil},
		{"answered at S: incorporated instead", at(S, records.StateAnswered), nil, false, nil},
		{"failed at S: incorporated instead", at(S, records.StateFailed), nil, false, nil},
		{"a fact at S: incorporated instead", fact(S), nil, false, nil},
		{"above S: a newer request owns the key", at(5, records.StateRequested), nil, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := newBus(t)
			var rev uint64
			if tc.cur != nil {
				rev = seed(t, bus, tc.cur)
			}
			q := records.NewRequester(bus.KV(bucket), spec())
			next, err := q.Withdraw(t.Context(), key, rev, S, tc.opts...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wrote, next != 0)
			after := read(t, bus)
			switch {
			case tc.wrote:
				require.True(t, after.ok)
				assert.Equal(t, next, after.rev)
				assert.EqualValues(t, S, after.rec.Seq)
				assert.Equal(t, records.StateWithdrawn, after.rec.State)
				assert.Equal(t, file.UID, after.rec.MediaFile.UID, "the header names the file even over an absent record")
			case tc.cur == nil:
				assert.False(t, after.ok)
			default:
				assert.Equal(t, rev, after.rev, "nothing was written")
				assert.Equal(t, tc.cur.Seq, after.rec.Seq)
				assert.Equal(t, tc.cur.State, after.rec.State)
			}
		})
	}
}

// Facts are never overwritten (loop spec §4.7): a transcode that already
// swapped the file is recorded even after a withdrawal, and only the caller
// that has incorporated it may replace it.
func TestRequestNeverOverwritesAnUnincorporatedFact(t *testing.T) {
	bus := newBus(t)
	f := at(4, records.StateAnswered)
	f.Swapped = true
	rev := seed(t, bus, f)
	q := records.NewRequester(bus.KV(bucket), spec())
	for _, opts := range [][]records.Option{nil, {records.IncorporatedThrough(3)}} {
		_, err := q.Request(t.Context(), key, rev, at(5, ""), opts...)
		require.ErrorIs(t, err, records.ErrUnincorporatedFact)
		got := read(t, bus)
		assert.EqualValues(t, 4, got.rec.Seq)
		assert.True(t, got.rec.Swapped, "the fact stands")
	}
	_, err := q.Request(t.Context(), key, rev, at(5, ""), records.IncorporatedThrough(4))
	require.NoError(t, err)
	assert.EqualValues(t, 5, read(t, bus).rec.Seq)

	plain := newBus(t)
	rev = seed(t, plain, at(4, records.StateAnswered))
	_, err = records.NewRequester(plain.KV(bucket), spec()).Request(t.Context(), key, rev, at(5, ""))
	require.NoError(t, err, "an answer that is no fact is replaced without a word")
}
