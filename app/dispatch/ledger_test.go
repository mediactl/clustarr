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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events"
)

// searchNormal is catalog-search-normal: MaxAckPending 8 in events.Default(),
// so its budget is 16.
const searchNormal = events.ConsumerCatalogSearchNorm

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func movieKey(name string) dispatch.Key {
	return dispatch.Key{Kind: "Movie", Namespace: "media", Name: name, Sub: "search"}
}

// newLedger is a ledger over the default topology at clk's time.
func newLedger(clk clockwork.Clock, o dispatch.Options) *dispatch.Ledger {
	if len(o.Topology.Consumers) == 0 {
		o.Topology = events.Default()
	}
	o.Now = clk.Now
	return dispatch.New(o)
}

// source is a dispatch.Source with a fixed answer; with release set, Rebuild
// waits for it (or the context), and called sees every call.
type source struct {
	durable string
	outs    []dispatch.Outstanding
	err     error
	called  chan struct{}
	release chan struct{}
}

func (s *source) Durable() string { return s.durable }

func (s *source) Rebuild(ctx context.Context, _ client.Reader) ([]dispatch.Outstanding, error) {
	if s.called != nil {
		s.called <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.outs, s.err
}

// TestAdmitHoldsTheBudgetAndReportsTheReason: a durable takes 2 × MaxAckPending
// dispatches, reserved or published; the next is refused Budget with the
// message its CR's delivery shows (§5.4). A key asking again holds its slot,
// a new seq supersedes its old one rather than taking a second, and an
// answer or a forgotten key frees one.
func TestAdmitHoldsTheBudgetAndReportsTheReason(t *testing.T) {
	ctx := t.Context()
	clk := clockwork.NewFakeClockAt(t0)
	l := newLedger(clk, dispatch.Options{})
	require.NoError(t, l.Rebuild(ctx))
	require.Equal(t, 16, l.Budget(searchNormal))

	for i := range 16 {
		k := movieKey(fmt.Sprintf("m%02d", i))
		require.True(t, l.Admit(searchNormal, k, 1).Admitted, "dispatch %d of 16", i+1)
		if i%2 == 0 {
			l.Published(searchNormal, k, 1)
		}
	}
	assert.Equal(t, 16, l.Outstanding(searchNormal))

	d := l.Admit(searchNormal, movieKey("m16"), 1)
	assert.Equal(t, dispatch.Decision{
		Reason:     catalogv1alpha1.DeliveryReasonBudget,
		Message:    "waiting for catalog-search-normal: 16 in flight",
		RetryAfter: dispatch.TickInterval,
	}, d)
	l.Waiting(searchNormal, movieKey("m16"), d.Reason)
	assert.Equal(t, 1, l.WaitingCount(searchNormal), "the planner's waiting key is counted")

	assert.True(t, l.Admit(searchNormal, movieKey("m00"), 1).Admitted, "a key asking again keeps its slot")
	assert.True(t, l.Admit(searchNormal, movieKey("m01"), 2).Admitted, "a new seq supersedes the key's older dispatch")
	assert.Equal(t, 16, l.Outstanding(searchNormal), "neither took a second slot")
	assert.True(t, l.Admit(events.ConsumerCatalogSearchHigh, movieKey("m16"), 1).Admitted,
		"another durable's budget is its own")

	l.Answered(searchNormal, movieKey("m01"), 1)
	assert.False(t, l.Admit(searchNormal, movieKey("m16"), 1).Admitted,
		"an answer below the slot's seq frees nothing")
	l.Answered(searchNormal, movieKey("m02"), 1)
	assert.True(t, l.Admit(searchNormal, movieKey("m16"), 1).Admitted, "an answer frees a slot")
	assert.Zero(t, l.WaitingCount(searchNormal), "an admission clears the key's waiting mark")

	assert.False(t, l.Admit(searchNormal, movieKey("m17"), 1).Admitted)
	l.Forget(movieKey("m03"))
	assert.True(t, l.Admit(searchNormal, movieKey("m17"), 1).Admitted, "a forgotten CR frees its slot")

	d = l.Admit("no-such-durable", movieKey("m00"), 1)
	assert.False(t, d.Admitted)
	assert.Equal(t, catalogv1alpha1.DeliveryReasonBudget, d.Reason)
	assert.Contains(t, d.Message, "no-such-durable is not a durable of the topology")
	assert.Zero(t, l.Budget("no-such-durable"))

	engine := events.EngineConsumer("qbit", 1)
	assert.Equal(t, 2*max(engine.MaxAckPending, 1), l.Budget(engine.Name),
		"an engine instance's durable is outside the static topology and still has its family's budget")
}

// TestAdmitRefusesUntilRebuilt: until Start has rebuilt every registered
// Source from status, Admit refuses Rebuilding (§8.4) -- a failed rebuild
// included -- and once it has, the outstanding dispatches status named hold
// their slots.
func TestAdmitRefusesUntilRebuilt(t *testing.T) {
	ctx := t.Context()
	clk := clockwork.NewFakeClockAt(t0)
	rebuilding := dispatch.Decision{
		Reason:     catalogv1alpha1.DeliveryReasonRebuilding,
		Message:    "waiting for the dispatch ledger to rebuild from status after a leader change",
		RetryAfter: dispatch.TickInterval,
	}

	failing := newLedger(clk, dispatch.Options{})
	failing.Register(&source{durable: searchNormal, err: errors.New("cache not synced")})
	assert.Equal(t, rebuilding, failing.Admit(searchNormal, movieKey("m00"), 1), "before Start")
	require.Error(t, failing.Rebuild(ctx))
	assert.False(t, failing.Rebuilt())
	assert.Equal(t, rebuilding, failing.Admit(searchNormal, movieKey("m00"), 1), "after a failed rebuild")

	src := &source{
		durable: searchNormal, called: make(chan struct{}, 1), release: make(chan struct{}),
		outs: []dispatch.Outstanding{{Key: movieKey("a"), Seq: 3}, {Key: movieKey("b"), Seq: 1}},
	}
	l := newLedger(clk, dispatch.Options{})
	l.Register(src)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- l.Start(runCtx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	<-src.called
	assert.False(t, l.Rebuilt())
	assert.Equal(t, rebuilding, l.Admit(searchNormal, movieKey("c"), 1), "while the rebuild reads status")
	close(src.release)
	require.Eventually(t, l.Rebuilt, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, 2, l.Outstanding(searchNormal), "status's dispatches hold their slots")
	assert.True(t, l.Admit(searchNormal, movieKey("c"), 1).Admitted)
	assert.True(t, l.Admit(searchNormal, movieKey("a"), 3).Admitted, "a's dispatch at 3 is already held")
	assert.Equal(t, 3, l.Outstanding(searchNormal))
}

// TestAnUnconfirmedReservationLapses: an admission not confirmed by Published
// within ReservationTTL lapses (the apply that would have issued the seq
// failed); a lapsed reservation that superseded a published dispatch gives
// that dispatch back; a published dispatch of a rebuildable durable stays
// until answered, and one of a durable with no Source lapses after AckWait ×
// MaxDeliver (ruling R23).
func TestAnUnconfirmedReservationLapses(t *testing.T) {
	ctx := t.Context()
	clk := clockwork.NewFakeClockAt(t0)
	l := newLedger(clk, dispatch.Options{})
	l.Register(&source{durable: searchNormal})
	require.NoError(t, l.Rebuild(ctx))

	for i := range 16 {
		require.True(t, l.Admit(searchNormal, movieKey(fmt.Sprintf("m%02d", i)), 1).Admitted)
	}
	assert.False(t, l.Admit(searchNormal, movieKey("m16"), 1).Admitted)
	clk.Advance(dispatch.ReservationTTL - time.Second)
	assert.Equal(t, 16, l.Outstanding(searchNormal), "a reservation holds for its whole TTL")
	assert.False(t, l.Admit(searchNormal, movieKey("m16"), 1).Admitted)
	clk.Advance(time.Second)
	assert.Zero(t, l.Outstanding(searchNormal), "every unconfirmed reservation lapsed")
	require.True(t, l.Admit(searchNormal, movieKey("m16"), 1).Admitted)

	l.Published(searchNormal, movieKey("m16"), 1)
	clk.Advance(time.Hour)
	assert.Equal(t, 1, l.Outstanding(searchNormal), "a published dispatch of a rebuildable durable never lapses")

	require.True(t, l.Admit(searchNormal, movieKey("m16"), 2).Admitted)
	clk.Advance(dispatch.ReservationTTL)
	assert.Equal(t, 1, l.Outstanding(searchNormal), "the superseded dispatch is still in flight")
	l.Answered(searchNormal, movieKey("m16"), 1)
	assert.Zero(t, l.Outstanding(searchNormal), "the slot that came back is seq 1's")

	meta, ok := events.Default().Consumer(events.ConsumerCatalogMetadata)
	require.True(t, ok)
	after := meta.AckWait * time.Duration(meta.MaxDeliver)
	k := dispatch.Key{Kind: "Movie", Namespace: "media", Name: "heat"}
	require.True(t, l.Admit(meta.Name, k, 1).Admitted)
	l.Published(meta.Name, k, 1)
	clk.Advance(after - time.Second)
	assert.Equal(t, 1, l.Outstanding(meta.Name))
	clk.Advance(time.Second)
	assert.Zero(t, l.Outstanding(meta.Name), "an unrebuildable kind's dispatch lapses after AckWait × MaxDeliver")
}
