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

package dispatch_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/presence"
)

// states is a dispatch.ConsumerStater over a fixed table; a durable it does
// not know is a failed read.
type states struct {
	mu sync.Mutex
	m  map[string]events.ConsumerState
}

func (s *states) set(durable string, st events.ConsumerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]events.ConsumerState{}
	}
	s.m[durable] = st
}

func (s *states) ConsumerState(_ context.Context, _, durable string) (events.ConsumerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[durable]
	if !ok {
		return events.ConsumerState{}, fmt.Errorf("no state for %s", durable)
	}
	return st, nil
}

// world is one leader's view: a fake clock, the durables' states, the
// progress bucket presence keys live in, and the Warnings it records.
type world struct {
	clk      *clockwork.FakeClock
	states   *states
	bus      *membus.Bus
	recorder *k8sevents.FakeRecorder
	l        *dispatch.Ledger
}

func newWorld(t *testing.T) *world {
	t.Helper()
	clk := clockwork.NewFakeClockAt(t0)
	bus := membus.New(clk)
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	w := &world{clk: clk, states: &states{}, bus: bus, recorder: k8sevents.NewFakeRecorder(16)}
	w.l = newLedger(clk, dispatch.Options{
		States:   w.states,
		Presence: &presence.Reader{KV: bus.KV(events.BucketProgress), Now: clk.Now},
		Recorder: w.recorder,
		Pod:      types.NamespacedName{Namespace: "clustarr", Name: "manager-0"},
	})
	require.NoError(t, w.l.Rebuild(t.Context()))
	return w
}

// present runs a real presence writer for domain until the test ends.
func (w *world) present(t *testing.T, domain string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	pw := &presence.Writer{
		KV: w.bus.KV(events.BucketProgress), Domain: domain, Pod: domain + "-0",
		Now: w.clk.Now, Interval: time.Hour,
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = pw.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		_, err := w.bus.KV(events.BucketProgress).Get(t.Context(), pw.Key())
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
}

// at advances the clock by d and runs one tick.
func (w *world) at(t *testing.T, d time.Duration) {
	t.Helper()
	w.clk.Advance(d)
	w.l.Tick(t.Context())
}

// warnings drains the Warnings recorded so far.
func (w *world) warnings() []string {
	var out []string
	for {
		select {
		case e := <-w.recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// fill publishes a durable's whole budget.
func (w *world) fill(t *testing.T, durable string) {
	t.Helper()
	for i := range w.l.Budget(durable) {
		k := movieKey(fmt.Sprintf("fill-%02d", i))
		require.True(t, w.l.Admit(durable, k, 1).Admitted)
		w.l.Published(durable, k, 1)
	}
}

// unattendedGauge reads clustarr_dispatch_unattended{consumer=durable}.
func unattendedGauge(t *testing.T, durable string) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, metrics.DispatchUnattended.WithLabelValues(durable).Write(&m))
	return m.GetGauge().GetValue()
}

// TestUnattendedNeedsTwoQuietMinutesOrNoPresence holds §5.5's detection: a
// durable is unattended after two minutes with Pending > 0, no pull open and
// an unchanged lag, or after two minutes with lag and no presence key for its
// domain -- never sooner, never for an autoscaled domain at zero with nothing
// queued, and never while a pull is open and the domain present. Each
// transition sets clustarr_dispatch_unattended and records one Warning; a
// full durable that is unattended refuses NoAgent rather than Budget.
func TestUnattendedNeedsTwoQuietMinutesOrNoPresence(t *testing.T) {
	t.Run("pending, no pull open and no progress for two minutes", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Catalog)
		w.states.set(searchNormal, events.ConsumerState{Pending: 3, MaxAckPending: 8})
		w.fill(t, searchNormal)

		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter-time.Second)
		assert.False(t, w.l.Unattended(searchNormal), "one second short of two minutes")
		assert.Equal(t, catalogv1alpha1.DeliveryReasonBudget, w.l.Admit(searchNormal, movieKey("x"), 1).Reason)
		assert.Zero(t, unattendedGauge(t, searchNormal))

		w.at(t, time.Second)
		assert.True(t, w.l.Unattended(searchNormal))
		assert.Equal(t, 1.0, unattendedGauge(t, searchNormal))
		assert.Equal(t, dispatch.Decision{
			Reason:     catalogv1alpha1.DeliveryReasonNoAgent,
			Message:    "no agent is taking catalog-search-normal: 16 in flight",
			RetryAfter: dispatch.TickInterval,
		}, w.l.Admit(searchNormal, movieKey("x"), 1))
		w.at(t, dispatch.TickInterval)
		ws := w.warnings()
		require.Len(t, ws, 1, "one Warning per transition, not per tick")
		assert.Contains(t, ws[0], "Warning "+dispatch.ReasonDispatchUnattended+" catalog-search-normal is unattended: 3 pending with no pull open")

		w.states.set(searchNormal, events.ConsumerState{Pending: 2, MaxAckPending: 8})
		w.at(t, dispatch.TickInterval)
		assert.False(t, w.l.Unattended(searchNormal), "progress makes it attended again")
		assert.Zero(t, unattendedGauge(t, searchNormal))
		assert.Empty(t, w.warnings(), "attended again records no Warning")
	})

	t.Run("a moving lag restarts the two minutes", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Catalog)
		w.states.set(searchNormal, events.ConsumerState{Pending: 3})
		w.at(t, 0)
		w.at(t, 90*time.Second)
		w.states.set(searchNormal, events.ConsumerState{Pending: 3, AckPending: 1})
		w.at(t, 0)
		w.at(t, 90*time.Second)
		assert.False(t, w.l.Unattended(searchNormal), "90 s since the lag last moved")
		w.at(t, 30*time.Second)
		assert.True(t, w.l.Unattended(searchNormal))
	})

	t.Run("an open pull is attendance", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Catalog)
		w.states.set(searchNormal, events.ConsumerState{Pending: 3, Waiting: 1})
		w.at(t, 0)
		w.at(t, 10*time.Minute)
		assert.False(t, w.l.Unattended(searchNormal))
		assert.Empty(t, w.warnings())
	})

	t.Run("lag and no presence key for two minutes", func(t *testing.T) {
		w := newWorld(t)
		w.states.set(searchNormal, events.ConsumerState{AckPending: 2, Waiting: 1})
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter-time.Second)
		assert.False(t, w.l.Unattended(searchNormal), "the HPA gets two minutes to wake the domain")
		w.at(t, time.Second)
		assert.True(t, w.l.Unattended(searchNormal))
		ws := w.warnings()
		require.Len(t, ws, 1)
		assert.Contains(t, ws[0], "no catalog agent present for 2m0s")
	})

	t.Run("an autoscaled domain at zero with nothing queued is not unattended", func(t *testing.T) {
		w := newWorld(t)
		w.states.set(searchNormal, events.ConsumerState{})
		w.at(t, 0)
		w.at(t, 10*time.Minute)
		assert.False(t, w.l.Unattended(searchNormal))
		assert.Empty(t, w.warnings())
	})

	t.Run("a failed state read keeps the last verdict", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Catalog)
		w.states.set(searchNormal, events.ConsumerState{Pending: 3})
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter)
		require.True(t, w.l.Unattended(searchNormal))
		w.states.mu.Lock()
		delete(w.states.m, searchNormal)
		w.states.mu.Unlock()
		w.at(t, dispatch.TickInterval)
		assert.True(t, w.l.Unattended(searchNormal))
	})
}

// TestNoRespondersReadsAsTheDomainsNoAgent (ADR-0020 §4, Wave N N4): a
// request to a verb that finds no responder is evidence its serving domain
// is absent even while a presence key says a pod lives, so the domain's
// durables with lag read NoAgent after UnattendedAfter. Any answer clears it,
// a timeout proves nothing, a verb no domain serves is ignored, and the
// evidence goes stale after 2 × UnattendedAfter with no new request.
func TestNoRespondersReadsAsTheDomainsNoAgent(t *testing.T) {
	meta := events.ConsumerCatalogMetadata
	lagging := events.ConsumerState{AckPending: 1, Waiting: 1} // lag, a pull open: not stalled
	noResponders := fmt.Errorf("metadata lookup: %w", events.ErrNoResponders)

	t.Run("no responders for two minutes is NoAgent; an answer clears it", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Metadata)
		w.states.set(meta, lagging)
		w.fill(t, meta)

		w.l.ObserveRequest(events.RPCMetadataLookup, noResponders)
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter-time.Second)
		assert.False(t, w.l.Unattended(meta))
		w.l.ObserveRequest(events.RPCMetadataSearch, context.DeadlineExceeded) // proves nothing
		w.at(t, time.Second)
		assert.True(t, w.l.Unattended(meta), "the presence key notwithstanding")
		assert.Equal(t, catalogv1alpha1.DeliveryReasonNoAgent, w.l.Admit(meta, movieKey("x"), 1).Reason)
		ws := w.warnings()
		require.Len(t, ws, 1)
		assert.Contains(t, ws[0], "no metadata agent present")

		w.l.ObserveRequest(events.RPCMetadataSearch, nil)
		w.at(t, dispatch.TickInterval)
		assert.False(t, w.l.Unattended(meta), "an answered request proves a responder")
		assert.Equal(t, catalogv1alpha1.DeliveryReasonBudget, w.l.Admit(meta, movieKey("x"), 1).Reason)
	})

	t.Run("a busy refusal is an answer", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Metadata)
		w.states.set(meta, lagging)
		w.l.ObserveRequest(events.RPCMetadataLookup, noResponders)
		w.l.ObserveRequest(events.RPCMetadataLookup, fmt.Errorf("lookup: %w", events.ErrResponderBusy))
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter)
		assert.False(t, w.l.Unattended(meta))
	})

	t.Run("a verb no domain serves says nothing", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Metadata)
		w.states.set(meta, lagging)
		w.l.ObserveRequest("clustarr.rpc.nothing.serves.this", noResponders)
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter)
		assert.False(t, w.l.Unattended(meta))
	})

	t.Run("old evidence goes stale", func(t *testing.T) {
		w := newWorld(t)
		w.present(t, agentdomain.Metadata)
		w.states.set(meta, lagging)
		w.l.ObserveRequest(events.RPCMetadataLookup, noResponders)
		w.clk.Advance(2*dispatch.UnattendedAfter + time.Second)
		w.at(t, 0)
		w.at(t, dispatch.UnattendedAfter)
		assert.False(t, w.l.Unattended(meta), "nothing asked the domain for over four minutes")
	})
}
