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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/records"
)

// TestAnswerRules is loop spec §4.8's table for a task at Seq 4: what
// Superseded says before work, what Claim (live lease, and a redelivery with
// none) and Defer do, and what Answer does with a plain answer and a fact.
func TestAnswerRules(t *testing.T) {
	const S = 4
	c := newClock()
	deferred := func(until time.Time) *rec { r := at(S, records.StateDeferred); r.DeferredUntil = &until; return r }
	W, N, D, F := records.Wrote, records.NotYet, records.Dropped, records.DroppedFact
	for _, tc := range []struct {
		name                                    string
		cur                                     *rec
		superseded                              bool
		claim, reclaim, deferV, answer, factAns records.Verdict
	}{
		{"absent: expired, or the bucket was lost", nil, false, N, N, N, W, W},
		{"requested below S", at(3, records.StateRequested), false, N, N, N, W, W},
		{"answered below S", at(3, records.StateAnswered), false, N, N, N, W, W},
		{"requested at S", at(S, records.StateRequested), false, W, W, W, W, W},
		{"claimed at S", at(S, records.StateClaimed), false, D, W, D, W, W},
		{"deferred at S, not yet due", deferred(c.Now().Add(time.Hour)), false, D, D, D, W, W},
		{"deferred at S, past DeferredUntil", deferred(c.Now().Add(-time.Minute)), false, W, W, W, W, W},
		{"answered at S", at(S, records.StateAnswered), true, D, D, D, D, D},
		{"failed at S", at(S, records.StateFailed), true, D, D, D, D, D},
		{"withdrawn at S", at(S, records.StateWithdrawn), true, D, D, D, D, W},
		{"above S", at(5, records.StateRequested), true, D, D, D, D, F},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := func(t *testing.T) (*membus.Bus, *records.Answerer[*rec]) {
				bus := newBus(t)
				if tc.cur != nil {
					seed(t, bus, tc.cur)
				}
				sp := spec()
				sp.Now = c.Now
				return bus, records.NewAnswerer(bus.KV(bucket), sp, "worker-0", "v-test")
			}
			unchanged := func(t *testing.T, bus *membus.Bus) {
				t.Helper()
				after := read(t, bus)
				if tc.cur == nil {
					assert.False(t, after.ok)
					return
				}
				assert.Equal(t, tc.cur.Seq, after.rec.Seq)
				assert.Equal(t, tc.cur.State, after.rec.State)
				assert.Empty(t, after.rec.Writer, "no worker wrote")
			}
			check := func(t *testing.T, name string, want records.Verdict, op func(context.Context, *records.Answerer[*rec]) (records.Verdict, error), state string) {
				t.Helper()
				bus, a := fresh(t)
				v, err := op(t.Context(), a)
				require.NoError(t, err, name)
				assert.Equal(t, want, v, "%s: got %s, want %s", name, v, want)
				if v != records.Wrote {
					unchanged(t, bus)
					return
				}
				after := read(t, bus)
				require.True(t, after.ok, name)
				assert.EqualValues(t, S, after.rec.Seq, name)
				assert.Equal(t, state, after.rec.State, name)
				assert.Equal(t, "worker-0", after.rec.Writer, name)
				assert.Equal(t, "v-test", after.rec.WriterVersion, name)
				assert.Equal(t, "in", after.rec.Inputs, "%s keeps the task's inputs", name)
			}

			_, a := fresh(t)
			got, err := a.Superseded(t.Context(), key, S)
			require.NoError(t, err)
			assert.Equal(t, tc.superseded, got, "Superseded")

			check(t, "Claim", tc.claim, func(ctx context.Context, a *records.Answerer[*rec]) (records.Verdict, error) {
				return a.Claim(ctx, key, S, false)
			}, records.StateClaimed)
			check(t, "Claim of a redelivery with no live lease", tc.reclaim, func(ctx context.Context, a *records.Answerer[*rec]) (records.Verdict, error) {
				return a.Claim(ctx, key, S, true)
			}, records.StateClaimed)
			check(t, "Defer", tc.deferV, func(ctx context.Context, a *records.Answerer[*rec]) (records.Verdict, error) {
				return a.Defer(ctx, key, S, c.Now().Add(10*time.Minute))
			}, records.StateDeferred)
			check(t, "Answer", tc.answer, func(ctx context.Context, a *records.Answerer[*rec]) (records.Verdict, error) {
				ans := at(S, records.StateAnswered)
				ans.Result = "done"
				return a.Answer(ctx, key, ans)
			}, records.StateAnswered)
			check(t, "Answer with a fact", tc.factAns, func(ctx context.Context, a *records.Answerer[*rec]) (records.Verdict, error) {
				ans := at(S, records.StateAnswered)
				ans.Result, ans.Swapped = "swapped", true
				return a.Answer(ctx, key, ans)
			}, records.StateAnswered)
		})
	}

	t.Run("an answer must be terminal", func(t *testing.T) {
		bus := newBus(t)
		seed(t, bus, at(S, records.StateRequested))
		a := records.NewAnswerer(bus.KV(bucket), spec(), "worker-0", "v-test")
		_, err := a.Answer(t.Context(), key, at(S, records.StateClaimed))
		require.Error(t, err)
		assert.Equal(t, records.StateRequested, read(t, bus).rec.State)
	})
}

// TestAnswerReReadsBeforeItsCAS: a worker read a requested record, then lost
// its compare-and-swap to the loop's newer request. Answer reads again and
// drops its answer (the lost-update rule), and a fact is dropped with the
// verdict the worker logs at Warn instead of overwriting the new request.
func TestAnswerReReadsBeforeItsCAS(t *testing.T) {
	for _, fact := range []bool{false, true} {
		t.Run(fmt.Sprint("fact=", fact), func(t *testing.T) {
			bus := newBus(t)
			seed(t, bus, at(4, records.StateRequested))
			loop := records.NewRequester(bus.KV(bucket), spec())
			kv := &interleavedKV{KV: bus.KV(bucket)}
			kv.after = func() {
				_, rev, _, err := loop.Get(context.Background(), key)
				require.NoError(t, err)
				_, err = loop.Request(context.Background(), key, rev, at(5, ""))
				require.NoError(t, err)
			}
			a := records.NewAnswerer[*rec](kv, spec(), "worker-0", "v-test")
			ans := at(4, records.StateAnswered)
			ans.Swapped = fact
			v, err := a.Answer(t.Context(), key, ans)
			require.NoError(t, err)
			want := records.Dropped
			if fact {
				want = records.DroppedFact
			}
			assert.Equal(t, want, v)
			got := read(t, bus)
			assert.EqualValues(t, 5, got.rec.Seq)
			assert.Equal(t, records.StateRequested, got.rec.State)
			assert.EqualValues(t, 2, kv.gets.Load(), "Answer read again after its compare-and-swap missed")
		})
	}
}
