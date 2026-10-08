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
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// gate is a handler body that holds until released or, unless deaf, until
// its context ends.
type gate struct {
	mu                      sync.Mutex
	running, most, finished int
	release                 chan struct{}
	once                    sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

func (g *gate) handler(ctx context.Context, _ events.Message) error {
	g.mu.Lock()
	g.running++
	g.most = max(g.most, g.running)
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	g.mu.Lock()
	g.running--
	g.finished++
	g.mu.Unlock()
	return nil
}

func (g *gate) read() (running, most, finished int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running, g.most, g.finished
}

// testHoldsNoMoreThanItsSlots: a subscription takes only as many deliveries
// as it has slots, however high the durable's cap, which is for every replica
// together (spec §9.3). natsbus's Consume pulled again as soon as its
// non-blocking callback returned and parked the rest: 5 held for one slot.
func testHoldsNoMoreThanItsSlots(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-slots",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1, MaxAckPending: 8,
	}
	Bind(ctx, t, bus, Topology(), sub)
	g := newGate(t)
	stop, err := bus.Subscribe(ctx, sub, g.handler)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	for i := range 5 {
		publishFetch(ctx, t, bus, fmt.Sprintf("slots-%d", i))
	}
	waitUntil(t, "the one slot's handler", func() bool { r, _, _ := g.read(); return r == 1 })
	time.Sleep(2 * time.Second)
	if st := consumerState(ctx, t, sa, sub); st.AckPending != 1 || st.Pending != 4 {
		t.Errorf("with one slot held: %+v, want AckPending 1 and Pending 4", st)
	}
}

// testReplicasShareTheCap: two replicas of two slots each run four handlers
// under a cap of four. natsbus once let one replica's Consume take the whole
// cap and park half of it, so two ran.
func testReplicasShareTheCap(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-share",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 2, MaxAckPending: 4,
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
	for i := range 8 {
		publishFetch(ctx, t, bus, fmt.Sprintf("share-%d", i))
	}
	waitUntil(t, "four handlers across two subscriptions", func() bool { r, _, _ := g.read(); return r == 4 })
	time.Sleep(500 * time.Millisecond)
	if _, most, _ := g.read(); most != 4 {
		t.Errorf("at most %d ran at once, want 4", most)
	}
}

// testLapsedHandlerFreesItsSlot: a handler hung past the broker's deadline
// gives its slot back, so the next task runs (spec §9.3). The hung message
// hangs only on its first delivery: its redelivery is what JetStream hands
// the freed slot first, and it must not take the slot for good.
func testLapsedHandlerFreesItsSlot(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	const deadline = 2 * time.Second
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-lapse-slot",
		Filters: []string{events.FilterIndexRSS},
		AckWait: deadline, MaxDeliver: 5, Backoff: []time.Duration{deadline},
		MaxInFlight: 1, MaxAckPending: 8,
	}
	Bind(ctx, t, bus, Topology(), sub)
	release := make(chan struct{})
	var once sync.Once
	hung := make(chan struct{}, 1)
	handled := make(chan time.Time, 1)
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		switch {
		case m.Envelope().ID == "lapse-hung" && m.Attempt() == 1:
			hung <- struct{}{}
			<-release // deaf to its context, as a handler stuck in a syscall is
		case m.Envelope().ID == "lapse-next":
			select {
			case handled <- time.Now():
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // runs before stop
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-lapse-hung"), envelope("lapse-hung", "index.RssTask.v1", 0)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case <-hung:
	case <-time.After(Timeout):
		t.Fatal("the hung task was never delivered")
	}
	start := time.Now()
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-lapse-next"), envelope("lapse-next", "index.RssTask.v1", 1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case at := <-handled:
		if got := at.Sub(start); got > 3*deadline {
			t.Errorf("the next task ran %v after it was published, want within 3x the %v deadline", got, deadline)
		}
	case <-time.After(3 * deadline):
		t.Fatalf("the next task waited more than 3x the %v deadline behind a hung handler: its slot was never reclaimed", deadline)
	}
}

// testLapsesAreBounded: handlers that are slow, not hung, lapse at the
// broker's deadline like hung ones. A reclaim without the lapsed cap would
// fetch a replacement for every lapse, bounded only by MaxAckPending; with
// it a pod runs at most twice its slots (spec §9.3, "The bound").
func testLapsesAreBounded(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 90*time.Second)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-lapse-bound",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 20, Backoff: []time.Duration{time.Second},
		MaxInFlight: 2, MaxAckPending: 16,
	}
	Bind(ctx, t, bus, Topology(), sub)
	var mu sync.Mutex
	running, most := 0, 0
	done := map[string]bool{}
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(3 * time.Second) // slow past every deadline, and deaf to its context
		mu.Lock()
		running--
		done[m.Envelope().ID] = true
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	const published = 8
	for i := range published {
		if _, err := bus.Publish(ctx, events.WorkRSSSubject(fmt.Sprintf("idx-bound-%d", i)),
			envelope(fmt.Sprintf("bound-%d", i), "index.RssTask.v1", i)); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	for until := time.Now().Add(75 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		mu.Lock()
		n := len(done)
		mu.Unlock()
		if n == published {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("%d of %d slow tasks finished", n, published)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if most > 2*sub.MaxInFlight {
		t.Errorf("%d handlers ran at once with %d slots; lapsed handlers may add at most as many again", most, sub.MaxInFlight)
	}
	if most <= sub.MaxInFlight {
		t.Errorf("at most %d ran at once: no lapse ever freed a slot", most)
	}
}

// testDrain: once a subscription stops, a running handler keeps its context
// for up to Drain and its acknowledgement reaches the broker (spec §3.5.6,
// §9.3). Without Drain the context ends at once, as before.
func testDrain(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	for _, tc := range []struct {
		durable string
		drain   time.Duration
		wantErr error
	}{
		{"ct-drain", 2 * time.Second, nil},
		{"ct-no-drain", 0, context.Canceled},
	} {
		sub := events.Subscription{
			Stream: events.StreamWorkIndex, Durable: tc.durable,
			Filters: []string{events.FilterIndexRSS},
			AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
			MaxInFlight: 1, Drain: tc.drain,
		}
		Bind(ctx, t, bus, Topology(), sub)
		subCtx, cancelSub := context.WithCancel(ctx)
		started, stopped := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		var calls atomic.Int32
		stop, err := bus.Subscribe(subCtx, sub, func(hctx context.Context, _ events.Message) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-stopped
			time.Sleep(300 * time.Millisecond)
			result <- hctx.Err()
			return nil
		})
		if err != nil {
			t.Fatalf("Subscribe %s: %v", tc.durable, err)
		}
		if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-"+tc.durable), envelope(tc.durable, "index.RssTask.v1", 0)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		select {
		case <-started:
		case <-time.After(Timeout):
			t.Fatalf("%s: never delivered", tc.durable)
		}
		cancelSub()
		close(stopped)
		select {
		case err := <-result:
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Errorf("%s: the handler's context 300ms after the subscription stopped = %v, want %v", tc.durable, err, tc.wantErr)
			}
		case <-time.After(Timeout):
			t.Fatalf("%s: the handler never returned", tc.durable)
		}
		stop()
		waitUntil(t, tc.durable+"'s acknowledgement", func() bool {
			subjects, err := sa.Subjects(ctx, sub.Stream, events.WorkRSSSubject("idx-"+tc.durable))
			return err == nil && len(subjects) == 0
		})
		if n := calls.Load(); n != 1 {
			t.Errorf("%s: handled %d times, want once", tc.durable, n)
		}
		// JetStream refuses a second durable with the same filter on a
		// work-queue stream, so the next case's durable needs this one gone.
		if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
			t.Fatalf("DeleteSubscription %s: %v", tc.durable, err)
		}
	}
}

// testRebind: a running subscription whose durable is deleted and created
// again -- what a NATS restart does to a memory-backed stream until the
// manager re-ensures (spec §5.9) -- resumes on its own.
func testRebind(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-rebind",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1,
	}
	Bind(ctx, t, bus, Topology(), sub)
	got := newCollector()
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	publishFetch(ctx, t, bus, "rebind-1")
	got.waitFor(t, 1, "the first task")
	if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	time.Sleep(time.Second)
	Bind(ctx, t, bus, Topology(), sub) // the manager re-creates it
	publishFetch(ctx, t, bus, "rebind-2")
	got.waitFor(t, 2, "a task after the durable was re-created")
}
