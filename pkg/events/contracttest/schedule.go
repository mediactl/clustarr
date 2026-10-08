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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// testScheduledTaskKeepsItsID is E12 of the worker-pool research: two tasks
// published WithScheduleAt and refused by their handler. The research used
// grabs; the grab task is gone (the manager grabs since the A plan), so
// these are the index domain's scheduled RSS polls (app/indexer/rssschedule). The handler sees each
// task's own ID, and CLUSTARR_DLQ holds two copies on two subjects. Before M2
// it saw ["" ""] and the DLQ held none: both copies carried
// Nats-Schedule-Next and were refused with 10188.
func testScheduledTaskKeepsItsID(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-sched-id",
		Filters: []string{events.FilterIndexRSS},
		AckWait: 5 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{time.Second},
		MaxInFlight: 2,
	}
	Bind(ctx, t, bus, Topology(), sub)
	var mu sync.Mutex
	var seen []string
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		mu.Lock()
		seen = append(seen, m.Envelope().ID)
		mu.Unlock()
		return events.Discard("refused", nil)
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	due := time.Now().Add(time.Second)
	for _, id := range []string{"rss-idx-a", "rss-idx-b"} {
		if _, err := bus.Publish(ctx, events.WorkRSSSubject(id), envelope(id, "index.RssTask.v1", 0), events.WithScheduleAt(due)); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}
	dlq.waitFor(t, 2, "both scheduled tasks' dead-letter copies")
	mu.Lock()
	got := slices.Sorted(slices.Values(seen))
	mu.Unlock()
	if !slices.Equal(got, []string{"rss-idx-a", "rss-idx-b"}) {
		t.Errorf("the handler saw IDs %q, want each task's own", got)
	}
	subjects := map[string]bool{}
	for i := range dlq.len() {
		e, subject := dlq.at(i)
		subjects[subject] = true
		for k := range e.Headers {
			if strings.HasPrefix(strings.ToLower(k), "nats-schedule") {
				t.Errorf("dead letter %s carries the transport header %s", subject, k)
			}
		}
	}
	if len(subjects) != 2 {
		t.Errorf("the two copies landed on %d subject(s), want 2: %v", len(subjects), subjects)
	}
}

// testScheduledLapseIsDeadLettered: a scheduled task whose final delivery
// lapses is copied by the dead-letter watcher and its WorkQueue original
// deleted. Before M2 copyLapsed was refused on every retry and, at
// MaxDeliver -1, naked for ever.
func testScheduledLapseIsDeadLettered(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-sched-lapse",
		Filters: []string{events.FilterIndexRSS},
		AckWait: 500 * time.Millisecond, MaxDeliver: 2, Backoff: []time.Duration{500 * time.Millisecond},
		MaxInFlight: 3, // a slot stays free after the final delivery, so a pull waits
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
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-sched-lapse"), envelope("rss-sched-lapse", "index.RssTask.v1", 0),
		events.WithScheduleAt(time.Now().Add(time.Second))); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	dlq.waitFor(t, 1, "the dead-letter copy of a scheduled task whose final delivery lapsed")
	if e, _ := dlq.at(0); e.Header(events.HeaderDLQMsgID) != "rss-sched-lapse" {
		t.Errorf("the copy names the original %q, want rss-sched-lapse", e.Header(events.HeaderDLQMsgID))
	}
	waitUntil(t, "the original removed from its work queue", func() bool {
		s, err := sa.Subjects(ctx, sub.Stream, events.FilterIndexRSS)
		return err == nil && len(s) == 0
	})
}
