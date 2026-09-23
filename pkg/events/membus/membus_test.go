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

package membus_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func TestBusContract(t *testing.T) {
	contracttest.RunBusContract(t, func() events.Bus { return membus.New(nil) })
}

func TestHooksContract(t *testing.T) {
	contracttest.RunHooksContract(t,
		func() events.Bus { return membus.New(nil) },
		func(h events.Hooks) events.Bus { return membus.New(nil, membus.WithHooks(h)) },
	)
}

func TestPublishWithoutEnsure(t *testing.T) {
	bus := membus.New(nil)
	defer func() { _ = bus.Close() }()

	_, err := bus.Publish(context.Background(),
		events.CatalogItemSubject("movie", events.ActionAdded, "u1"),
		&events.Envelope{ID: "x"})
	if !errors.Is(err, events.ErrStreamNotFound) {
		t.Fatalf("Publish before Ensure error = %v, want ErrStreamNotFound", err)
	}
}

func TestEnsureRefusesRetentionChange(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	defer func() { _ = bus.Close() }()

	top := contracttest.Topology()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for i := range top.Streams {
		if top.Streams[i].Name == events.StreamWorkCatalogarr {
			top.Streams[i].Retention = events.RetentionLimits
		}
	}
	err := bus.Ensure(ctx, top)
	if !errors.Is(err, events.ErrRetentionImmutable) {
		t.Fatalf("Ensure with changed retention error = %v, want ErrRetentionImmutable", err)
	}
}

func TestQueueFullOnDiscardNew(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	defer func() { _ = bus.Close() }()

	// Work streams cannot use DiscardNew, because nats-server refuses to
	// pair it with message schedules, so back-pressure is exercised on the
	// releases stream instead.
	top := contracttest.Topology()
	for i := range top.Streams {
		if top.Streams[i].Name == events.StreamReleases {
			top.Streams[i].MaxBytes = 16
			top.Streams[i].Discard = events.DiscardNew
		}
	}
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	payload := make([]byte, 12)
	if _, err := bus.Publish(ctx, events.ReleaseSubject("torrent", "idx", 2000),
		&events.Envelope{ID: "a", Data: payload}); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	_, err := bus.Publish(ctx, events.ReleaseSubject("torrent", "idx", 5000),
		&events.Envelope{ID: "b", Data: payload})
	if !errors.Is(err, events.ErrQueueFull) {
		t.Fatalf("Publish into a full DiscardNew stream error = %v, want ErrQueueFull", err)
	}
}

func TestExpectStreamMismatch(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	defer func() { _ = bus.Close() }()
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	_, err := bus.Publish(ctx, events.CatalogItemSubject("movie", events.ActionAdded, "u1"),
		&events.Envelope{ID: "x"}, events.WithExpectStream(events.StreamReleases))
	if err == nil {
		t.Fatal("Publish with a mismatched WithExpectStream succeeded")
	}
}

// TestHungHandlerHoldingEverySlotIsDeadLettered is the saturated case the
// contract suite leaves out: the only handler slot is held by a handler hung
// on the message's first delivery, so the final delivery is claimed but can
// never run. JetStream expires it on the server whatever the client is doing;
// membus must too, rather than dead-letter only once a slot frees, which a
// hung handler never does.
func TestHungHandlerHoldingEverySlotIsDeadLettered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), contracttest.Timeout)
	defer cancel()
	bus := membus.New(nil)
	defer func() { _ = bus.Close() }()
	release := make(chan struct{})
	defer close(release) // before Close, which waits for handlers
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var mu sync.Mutex
	var dlq []*events.Envelope
	if _, err := bus.Subscribe(ctx, events.Subscription{
		Stream: events.StreamDLQ, Durable: "dlq", Filters: []string{events.FilterAllDLQ},
		AckWait: time.Second, MaxInFlight: 4,
	}, func(_ context.Context, m events.Message) error {
		mu.Lock()
		dlq = append(dlq, m.Envelope())
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe DLQ: %v", err)
	}
	if _, err := bus.Subscribe(ctx, events.Subscription{
		Stream: events.StreamWorkIndexarr, Durable: "hung", Filters: []string{events.FilterIndexRSS},
		AckWait: 50 * time.Millisecond, MaxDeliver: 2, MaxInFlight: 1,
	}, func(ctx context.Context, _ events.Message) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("hung")
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-1"),
		&events.Envelope{ID: "task-1", Data: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(dlq)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a final delivery that lapsed behind a hung handler was never dead-lettered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(dlq) != 1 {
		t.Fatalf("dead-lettered %d copies, want exactly 1", len(dlq))
	}
	if got, want := dlq[0].Headers[events.HeaderDLQReason], events.AckWaitExhaustedReason(2); got != want {
		t.Errorf("%s = %q, want %q", events.HeaderDLQReason, got, want)
	}
	if got := dlq[0].Headers[events.HeaderDLQAttempts]; got != "2" {
		t.Errorf("%s = %q, want \"2\"", events.HeaderDLQAttempts, got)
	}
}
