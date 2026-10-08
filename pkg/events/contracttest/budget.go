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
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// testBusHeartbeats: with a budget, the bus sends InProgress every third of
// the deadline, so a handler running three deadlines is never redelivered.
func testBusHeartbeats(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 60*time.Second)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-budget-hb",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 5, Backoff: []time.Duration{time.Second},
		MaxInFlight: 1, MaxAckPending: 8, HandlerTimeout: 30 * time.Second,
	}
	Bind(ctx, t, bus, Topology(), sub)
	var mu sync.Mutex
	attempts := []uint64{}
	done := make(chan struct{})
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		mu.Lock()
		attempts = append(attempts, m.Attempt())
		mu.Unlock()
		time.Sleep(3500 * time.Millisecond) // three and a half deadlines, no heartbeat of its own
		close(done)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-budget-hb"), envelope("budget-hb", "index.RssTask.v1", 0)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	<-done
	time.Sleep(time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 1 {
		t.Errorf("deliveries %v, want exactly one: the bus heartbeats a budgeted handler", attempts)
	}
}

// testBudgetCancels: a handler past its budget sees ErrHandlerBudget as its
// context's cause; a deaf one is cancelled with ErrLapsed one deadline after
// it lapses; and Wedged names the subscription once it has sat at the lapsed
// cap past its budget.
func testBudgetCancels(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 60*time.Second)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-budget-cut",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 10, Backoff: []time.Duration{time.Second},
		MaxInFlight: 1, MaxAckPending: 8, HandlerTimeout: 2 * time.Second,
	}
	Bind(ctx, t, bus, Topology(), sub)
	causes := make(chan error, 4)
	release := make(chan struct{})
	var once sync.Once
	stop, err := bus.Subscribe(ctx, sub, func(hctx context.Context, m events.Message) error {
		if m.Envelope().ID == "budget-polite" {
			<-hctx.Done()
			causes <- context.Cause(hctx)
			return hctx.Err()
		}
		<-release // deaf
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-polite"), envelope("budget-polite", "index.RssTask.v1", 0)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case c := <-causes:
		if !errors.Is(c, events.ErrHandlerBudget) {
			t.Errorf("cause %v, want ErrHandlerBudget", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a handler past its 2 s budget was never cancelled")
	}
	w, ok := bus.(events.WedgeReporter)
	if !ok {
		t.Fatalf("%T does not implement events.WedgeReporter", bus)
	}
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-deaf"), envelope("budget-deaf", "index.RssTask.v1", 1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	waitUntil(t, "Wedged to name the subscription held by deaf handlers", func() bool {
		err := w.Wedged()
		return err != nil && strings.Contains(err.Error(), "ct-budget-cut")
	})
	once.Do(func() { close(release) })
	waitUntil(t, "Wedged to clear once the deaf handlers return", func() bool { return w.Wedged() == nil })
}

// testStreamFill: StreamFill reports a stream's bytes and its MaxBytes, the
// ratio QueueGauge exports because lag cannot show a memory stream discarding
// its oldest messages (S10).
func testStreamFill(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ss, ok := bus.(events.StreamStater)
	if !ok {
		t.Fatalf("%T does not implement events.StreamStater", bus)
	}
	spec, _ := Topology().Stream(events.StreamWorkIndex)
	for i := range 3 {
		if _, err := bus.Publish(ctx, events.WorkRSSSubject(fmt.Sprintf("fill-%d", i)), envelope(fmt.Sprintf("fill-%d", i), "index.RssTask.v1", i)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	f, err := ss.StreamFill(ctx, events.StreamWorkIndex)
	if err != nil {
		t.Fatalf("StreamFill: %v", err)
	}
	if f.Messages != 3 || f.Bytes == 0 || f.MaxBytes != uint64(spec.MaxBytes) {
		t.Errorf("StreamFill = %+v, want 3 messages, some bytes and MaxBytes %d", f, spec.MaxBytes)
	}
	if _, err := ss.StreamFill(ctx, "NO_SUCH_STREAM"); !errors.Is(err, events.ErrStreamNotFound) {
		t.Errorf("a missing stream: %v, want ErrStreamNotFound", err)
	}
}
