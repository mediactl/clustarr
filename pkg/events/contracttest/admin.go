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

package contracttest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// consumerState reads sub's durable through StreamAdmin, failing the test on
// an error.
func consumerState(ctx context.Context, t *testing.T, sa events.StreamAdmin, sub events.Subscription) events.ConsumerState {
	t.Helper()
	st, err := sa.ConsumerState(ctx, sub.Stream, sub.Durable)
	if err != nil {
		t.Fatalf("ConsumerState %s/%s: %v", sub.Stream, sub.Durable, err)
	}
	return st
}

// publishFetch publishes one subtitle fetch task with ID id.
func publishFetch(ctx context.Context, t *testing.T, bus events.Bus, id string, opts ...events.PublishOption) {
	t.Helper()
	if _, err := bus.Publish(ctx, events.WorkFetchSubject(events.PriorityNormal, id, "en"),
		envelope(id, "subtitle.FetchTask.v2", id), opts...); err != nil {
		t.Fatalf("Publish %s: %v", id, err)
	}
}

// testConsumerState holds StreamAdmin.ConsumerState to JetStream's ConsumerInfo
// (spec §9.2): Pending is matching and undelivered, AckPending delivered and
// unsettled -- running, or waiting out a delayed nak -- and a scheduled
// publish counts in neither until it fires.
func testConsumerState(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-state",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 2, MaxAckPending: 2,
	}
	Bind(ctx, t, bus, Topology(), sub)

	const published = 5
	for i := range published {
		publishFetch(ctx, t, bus, fmt.Sprintf("state-%d", i))
	}
	if st := consumerState(ctx, t, sa, sub); st.Pending != published || st.AckPending != 0 || st.MaxAckPending != 2 {
		t.Fatalf("before any Subscribe: %+v, want Pending %d, AckPending 0, MaxAckPending 2", st, published)
	}
	if st := consumerState(ctx, t, sa, sub); st.ObservedAt.IsZero() {
		t.Errorf("ConsumerState.ObservedAt is zero, want when the broker gathered the numbers")
	}

	verdicts := make(chan error, published)
	var mu sync.Mutex
	running := 0
	stop, err := bus.Subscribe(ctx, sub, func(ctx context.Context, _ events.Message) error {
		mu.Lock()
		running++
		mu.Unlock()
		select {
		case err := <-verdicts:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	waitUntil(t, "two deliveries held", func() bool {
		st := consumerState(ctx, t, sa, sub)
		return st.AckPending == 2 && st.Pending == published-2
	})

	// A delayed nak keeps its delivery ack-pending, so the cap still holds
	// and nothing else is delivered.
	verdicts <- events.Retry(time.Hour, errors.New("provider asked us to wait"))
	time.Sleep(500 * time.Millisecond)
	if st := consumerState(ctx, t, sa, sub); st.AckPending != 2 || st.Pending != published-2 {
		t.Errorf("after a one-hour nak: %+v, want AckPending 2, Pending %d", st, published-2)
	}
	mu.Lock()
	if running != 2 {
		t.Errorf("%d handlers started with MaxAckPending 2 held by one running and one naked delivery, want 2", running)
	}
	mu.Unlock()

	// A schedule in the future is on a holding subject no filter matches.
	publishFetch(ctx, t, bus, "state-later", events.WithScheduleAt(time.Now().Add(time.Hour)))
	time.Sleep(300 * time.Millisecond)
	if st := consumerState(ctx, t, sa, sub); st.Pending != published-2 {
		t.Errorf("a scheduled publish counted as Pending: %+v", st)
	}

	if _, err := sa.ConsumerState(ctx, sub.Stream, "ct-no-such-durable"); !errors.Is(err, events.ErrConsumerNotFound) {
		t.Errorf("ConsumerState of a missing durable = %v, want ErrConsumerNotFound", err)
	}
	if _, err := sa.ConsumerState(ctx, "CLUSTARR_NO_SUCH_STREAM", "x"); !errors.Is(err, events.ErrStreamNotFound) {
		t.Errorf("ConsumerState on a missing stream = %v, want ErrStreamNotFound", err)
	}
}

// testMaxAckPendingCapsEveryReplica: the durable's MaxAckPending bounds every
// subscription of it together (spec §9.3). membus had no consumer-wide cap.
func testMaxAckPendingCapsEveryReplica(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-cap",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 2, MaxAckPending: 2,
	}
	Bind(ctx, t, bus, Topology(), sub)

	g := newGate(t)
	for range 2 {
		stop, err := bus.Subscribe(ctx, sub, g.handler)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		defer stop()
	}
	const published = 6
	for i := range published {
		publishFetch(ctx, t, bus, fmt.Sprintf("cap-%d", i))
	}
	waitUntil(t, "the cap's two handlers", func() bool { r, _, _ := g.read(); return r == 2 })
	time.Sleep(time.Second)
	if _, most, _ := g.read(); most != 2 {
		t.Errorf("%d handlers ran at once across two subscriptions of a durable capped at 2", most)
	}
	g.open()
	waitUntil(t, "every task handled", func() bool { _, _, f := g.read(); return f == published })
}
