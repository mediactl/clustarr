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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// testSaturatedNeverDeadLettersUnrun is E11 of the worker-pool research
// (2026-10-07) as a test. A one-slot subscription whose slot, live and under
// the lapsed cap, is held by a handler deaf to its context fetches nothing
// more: queued tasks stay Pending, spend no delivery attempt and are never
// dead-lettered, and each runs once the deaf handlers return. Wave 4c's loop
// kept a Fetch(1) open at the lapsed cap and parked what arrived; every
// redelivery spent an attempt, and all eight were dead-lettered unrun in 17 s.
func testSaturatedNeverDeadLettersUnrun(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 90*time.Second)
	dlq := subscribeDLQ(ctx, t, bus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-saturated",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 3, Backoff: []time.Duration{time.Second},
		MaxInFlight: 1, MaxAckPending: 16,
	}
	Bind(ctx, t, bus, Topology(), sub)
	release := make(chan struct{})
	var once sync.Once
	var ran atomic.Int32
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		if strings.HasPrefix(m.Envelope().ID, "sat-hang") {
			<-release // deaf to its context, on every delivery
			return nil
		}
		ran.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // runs before stop
	publish := func(id string, i int) {
		t.Helper()
		if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-"+id), envelope(id, "index.RssTask.v1", i)); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}
	publish("sat-hang-1", 0)
	// Delivery 1 lapses after about 2 s and its redelivery hangs too: one
	// live, one lapsed, the cap reached with the one slot held.
	time.Sleep(5 * time.Second)
	for i := range 8 {
		publish(fmt.Sprintf("sat-work-%d", i), i+1)
	}
	time.Sleep(12 * time.Second) // E11 had dead-lettered all eight by now
	if n := ran.Load(); n != 0 {
		t.Fatalf("%d healthy tasks ran while a deaf handler held the slot; want 0", n)
	}
	for i := range dlq.len() {
		if e, _ := dlq.at(i); strings.HasPrefix(e.Header(events.HeaderDLQMsgID), "sat-work") {
			t.Fatalf("%s was dead-lettered without ever running: %s",
				e.Header(events.HeaderDLQMsgID), e.Header(events.HeaderDLQReason))
		}
	}
	if st := consumerState(ctx, t, sa, sub); st.Pending != 8 {
		t.Errorf("Pending = %d, want the 8 queued tasks left at the broker for other replicas", st.Pending)
	}
	once.Do(func() { close(release) })
	waitUntil(t, "every queued task to run once the deaf handlers returned", func() bool { return ran.Load() == 8 })
}

// testLapsedCannotExtend is E3 of the worker-pool research: the broker keys
// InProgress by stream sequence, so a lapsed handler that keeps heartbeating
// kept its redelivered copy from ever timing out. Delivery 1 lapses and
// heartbeats; delivery 2 hangs silent; delivery 3 must still arrive on the
// broker's schedule (S2).
func testLapsedCannotExtend(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 60*time.Second)
	const deadline = time.Second
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-muted",
		Filters: []string{events.FilterIndexRSS},
		AckWait: deadline, MaxDeliver: 5, Backoff: []time.Duration{deadline},
		MaxInFlight: 2, MaxAckPending: 8,
	}
	Bind(ctx, t, bus, Topology(), sub)
	release := make(chan struct{})
	var once, began sync.Once
	redelivered := make(chan struct{})
	second := make(chan time.Time, 1)
	third := make(chan time.Time, 1)
	stop, err := bus.Subscribe(ctx, sub, func(hctx context.Context, m events.Message) error {
		switch m.Attempt() {
		case 1:
			<-redelivered // silent until the broker redelivered it: delivery 1 has lapsed
			for {         // then heartbeats, deaf to its context, as E3's stale delivery did
				select {
				case <-release:
					return nil
				case <-time.After(300 * time.Millisecond):
					_ = m.InProgress(context.WithoutCancel(hctx))
				}
			}
		case 2:
			began.Do(func() { close(redelivered) })
			second <- time.Now()
			<-release // silent
			return nil
		default:
			select {
			case third <- time.Now():
			default:
			}
			return nil
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-muted"), envelope("muted-1", "index.RssTask.v1", 0)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var at2 time.Time
	select {
	case at2 = <-second:
	case <-time.After(10 * deadline):
		t.Fatal("delivery 2 never came: delivery 1 did not lapse")
	}
	select {
	case at3 := <-third:
		if d := at3.Sub(at2); d > 4*deadline {
			t.Errorf("delivery 3 came %v after delivery 2, want within 4x the %v deadline", d, deadline)
		}
	case <-time.After(5 * deadline):
		t.Fatal("delivery 3 never came: the lapsed delivery's InProgress kept the live copy from timing out")
	}
}

// testBoundTiming: a subscriber whose compiled Subscription disagrees with
// the durable it binds -- an older agent on a newer topology -- times lapses
// and settles on the broker's numbers, not its own (S5, D5).
func testBoundTiming(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 60*time.Second)
	dlq := subscribeDLQ(ctx, t, bus)
	bound := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-bound-timing",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 2, Backoff: []time.Duration{time.Second},
		MaxInFlight: 1, MaxAckPending: 8,
	}
	Bind(ctx, t, bus, Topology(), bound)
	caller := bound
	caller.AckWait, caller.Backoff, caller.MaxDeliver = 30*time.Second, []time.Duration{30 * time.Second}, 5
	release := make(chan struct{})
	var once sync.Once
	handled := make(chan time.Time, 1)
	stop, err := bus.Subscribe(ctx, caller, func(_ context.Context, m events.Message) error {
		switch m.Envelope().ID {
		case "bound-hung":
			if m.Attempt() == 1 {
				<-release
			}
			return nil
		case "bound-next":
			select {
			case handled <- time.Now():
			default:
			}
			return nil
		default: // bound-fail
			return errors.New("fails every time")
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	publish := func(id string) {
		t.Helper()
		if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-"+id), envelope(id, "index.RssTask.v1", 0)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	publish("bound-hung")
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	publish("bound-next")
	select {
	case at := <-handled:
		if d := at.Sub(start); d > 5*time.Second {
			t.Errorf("the next task ran after %v: the slot was timed on the caller's 30 s, not the durable's 1 s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hung handler's slot was never reclaimed on the durable's 1 s deadline")
	}
	publish("bound-fail")
	dlq.waitFor(t, 1, "the failing task dead-lettered at the durable's MaxDeliver 2")
	// Settle, judging on the bound MaxDeliver 2, dead-letters in process with
	// the handler's error. Judging on the caller's 5 it would nak for 30 s and
	// leave the broker to give up, and the watcher's copy would read
	// "acknowledgement timed out".
	if e, _ := dlq.at(0); !strings.Contains(e.Header(events.HeaderDLQReason), "fails every time") {
		t.Errorf("dead-lettered with %q, want Settle's own reason at the bound durable's MaxDeliver 2", e.Header(events.HeaderDLQReason))
	}
}
