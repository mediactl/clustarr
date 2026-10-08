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
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// testBindsNeverWrites: only the manager's EnsureTopology writes a static
// durable's config, so an older agent cannot revert a newer topology (spec
// §5.9). A Subscribe asking for other settings changes nothing, and creates
// no watcher the topology lacks.
func testBindsNeverWrites(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	ensured := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-bindonly",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 2, MaxAckPending: 8,
	}
	spec := events.SubscriptionSpec(ensured)
	top := Topology()
	top.Consumers = append(top.Consumers, spec) // no watcher, on purpose
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	asked := ensured
	asked.MaxInFlight, asked.MaxAckPending = 1, 1
	asked.AckWait, asked.Backoff = 5*time.Second, []time.Duration{5 * time.Second}
	got := newCollector()
	stop, err := bus.Subscribe(ctx, asked, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	publishFetch(ctx, t, bus, "bindonly-1")
	got.waitFor(t, 1, "a task on the bound durable")
	if st := consumerState(ctx, t, sa, ensured); st.MaxAckPending != 8 {
		t.Errorf("MaxAckPending after Subscribe = %d, want the topology's 8", st.MaxAckPending)
	}
	time.Sleep(time.Second)
	watcher := events.TopologyObject{
		Kind: events.TopologyConsumer, Stream: events.StreamAdvisories,
		Name: events.DeadLetterWatcherName(ensured.Stream, ensured.Durable),
	}.String()
	withWatcher := top
	withWatcher.Consumers = append(slices.Clone(top.Consumers), events.DeadLetterWatcherSpec(spec))
	missing, err := sa.Missing(ctx, withWatcher)
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if !slices.Equal(missing, []string{watcher}) {
		t.Errorf("Missing = %v, want only %s: Subscribe must not create its watcher", missing, watcher)
	}
}

// testWaitsForMissingDurable: a subscriber that starts before the manager
// idles instead of failing or creating, and starts once the durable exists.
func testWaitsForMissingDurable(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "ct-wait",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1,
	}
	got := newCollector()
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe on a durable not created yet = %v, want it to wait", err)
	}
	defer stop()
	publishFetch(ctx, t, bus, "wait-1")
	time.Sleep(time.Second)
	if n := got.len(); n != 0 {
		t.Fatalf("handled %d tasks with no durable", n)
	}
	for _, c := range [][2]string{
		{sub.Stream, sub.Durable},
		{events.StreamAdvisories, events.DeadLetterWatcherName(sub.Stream, sub.Durable)},
	} {
		names, err := sa.Subscriptions(ctx, c[0])
		if err != nil {
			t.Fatalf("Subscriptions %s: %v", c[0], err)
		}
		if slices.Contains(names, c[1]) {
			t.Errorf("Subscribe created %s on %s", c[1], c[0])
		}
	}
	Bind(ctx, t, bus, Topology(), sub)
	got.waitFor(t, 1, "the task once the manager created the durable")
}

// testBindOnlyDeadLetters: with Subscribe bind-only, a hung handler's final
// delivery is still dead-lettered, and removed from its work queue, by the
// subscription's own watcher -- and, once the subscription has stopped, by
// WatchDeadLetters alone, the manager's backstop (spec §5.9).
func testBindOnlyDeadLetters(t *testing.T, newBus func() events.Bus) {
	t.Run("WhileSubscribed", func(t *testing.T) {
		ctx, bus := setup(t, newBus)
		dlq := subscribeDLQ(ctx, t, bus)
		sa := admin(t, bus)
		// MaxInFlight above MaxDeliver, so a slot is still free after the
		// final delivery lapsed: JetStream raises MAX_DELIVERIES only on its
		// next delivery attempt to a waiting pull, and a subscription whose
		// every slot is held fetches nothing (split §9.3 as amended
		// 2026-10-07, M1). One slot held by this deaf handler would leave
		// the copy to the next pull from any replica, which
		// SaturatedSubscriptionNeverDeadLettersAnUnrunTask and the backstop
		// variant cover.
		sub := events.Subscription{
			Stream: events.StreamWorkIndex, Durable: "ct-bindonly-dlq",
			Filters: []string{events.FilterIndexRSS},
			AckWait: 500 * time.Millisecond, MaxDeliver: 2,
			Backoff: []time.Duration{500 * time.Millisecond}, MaxInFlight: 3,
		}
		Bind(ctx, t, bus, Topology(), sub)
		release := make(chan struct{})
		var once sync.Once
		stop, err := bus.Subscribe(ctx, sub, func(context.Context, events.Message) error {
			<-release
			return errors.New("hung")
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		t.Cleanup(stop)
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-bindonly"), envelope("bindonly-lapse", "index.RssTask.v1", 0)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		dlq.waitFor(t, 1, "the dead-letter copy")
		waitUntil(t, "the lapsed message removed from its work queue", func() bool {
			s, err := sa.Subjects(ctx, sub.Stream, events.FilterIndexRSS)
			return err == nil && len(s) == 0
		})
	})
	// The backstop variant, as W4.90 (split §9.3 as amended 2026-10-07, D4)
	// rewrote it: a pull the stopped subscription abandoned is not relied
	// on (worker-pool research E5), so the advisory comes from the next
	// replica's pull, and WatchDeadLetters alone -- the manager's backstop --
	// copies it.
	t.Run("ByTheBackstopOnceAReplicaPullsAgain", func(t *testing.T) {
		ctx, bus := setup(t, newBus)
		dlq := subscribeDLQ(ctx, t, bus)
		sa := admin(t, bus)
		const deadline = 2 * time.Second
		// MaxInFlight above MaxDeliver: every delivery hangs in its own slot.
		sub := events.Subscription{
			Stream: events.StreamWorkIndex, Durable: "ct-backstop",
			Filters: []string{events.FilterIndexRSS},
			AckWait: deadline, MaxDeliver: 2, Backoff: []time.Duration{deadline},
			MaxInFlight: 3,
		}
		Bind(ctx, t, bus, Topology(), sub)
		release := make(chan struct{})
		var once sync.Once
		var mu sync.Mutex
		attempts := 0
		stop, err := bus.Subscribe(ctx, sub, func(context.Context, events.Message) error {
			mu.Lock()
			attempts++
			mu.Unlock()
			<-release // deaf: the subscription's stop must not settle it
			return errors.New("hung")
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-backstop"), envelope("backstop-lapse", "index.RssTask.v1", 0)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		waitUntil(t, "the final delivery", func() bool { mu.Lock(); defer mu.Unlock(); return attempts >= 2 })
		stopped := make(chan struct{})
		go func() { stop(); close(stopped) }()
		t.Cleanup(func() { once.Do(func() { close(release) }); <-stopped })

		// The CONSUMER.INFO the manager's QueueGauge sends every 30 s prunes
		// the pull the stopped subscription abandoned (nats-server
		// consumer.go:3650-3653; worker-pool research E5): nothing raises
		// MAX_DELIVERIES on it.
		consumerState(ctx, t, sa, sub)

		w, ok := bus.(events.DeadLetterWatcher)
		if !ok {
			t.Fatalf("%T does not implement events.DeadLetterWatcher", bus)
		}
		stopWatch, err := w.WatchDeadLetters(ctx, sub)
		if err != nil {
			t.Fatalf("WatchDeadLetters: %v", err)
		}
		defer stopWatch()

		// A replica comes back. Its pull is what lets JetStream raise the
		// advisory (getNextMsg; MaxDeliver 2 is past the ack timer's reach).
		stop2, err := bus.Subscribe(ctx, sub, func(context.Context, events.Message) error { return nil })
		if err != nil {
			t.Fatalf("Subscribe the second replica: %v", err)
		}
		t.Cleanup(stop2)
		dlq.waitFor(t, 1, "the backstop's copy, once a replica pulled again")
		waitUntil(t, "the lapsed message removed from its work queue", func() bool {
			s, err := sa.Subjects(ctx, sub.Stream, events.FilterIndexRSS)
			return err == nil && len(s) == 0
		})
	})
}
