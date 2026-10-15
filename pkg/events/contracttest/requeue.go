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
	"strings"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// requeueSub is a RequeueOnStop durable whose Drain the stop must not wait
// out: its handlers are cancelled at once.
func requeueSub(durable string) events.Subscription {
	return events.Subscription{
		Stream: events.StreamWorkIndex, Durable: durable,
		Filters: []string{events.FilterIndexRSS},
		AckWait: 30 * time.Second, MaxDeliver: 2, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1, Drain: 10 * time.Second, RequeueOnStop: true,
	}
}

type requeueDelivery struct {
	attempt uint64
	env     *events.Envelope
}

// testRequeueOnStop: a RequeueOnStop subscription that stops cancels its
// running handler at once, with cause ErrStopping, and hands the delivery
// back as a fresh copy. Stopped more often than MaxDeliver, the task is
// delivered as attempt 1 every time, keeps its payload, key and headers,
// and is never dead-lettered; a subscriber that finishes it removes it.
func testRequeueOnStop(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)
	sa := admin(t, bus)
	sub := requeueSub("ct-requeue")
	Bind(ctx, t, bus, Topology(), sub)
	subject := events.WorkRSSSubject("idx-requeue")
	if _, err := bus.Publish(ctx, subject, envelope("task-requeue", "index.RssTask.v1", map[string]string{"k": "v"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var lastID string
	for i := range sub.MaxDeliver + 1 {
		subCtx, cancelSub := context.WithCancel(ctx)
		got := make(chan requeueDelivery, 1)
		cause := make(chan error, 1)
		stop, err := bus.Subscribe(subCtx, sub, func(hctx context.Context, m events.Message) error {
			got <- requeueDelivery{m.Attempt(), m.Envelope()}
			<-hctx.Done()
			cause <- context.Cause(hctx)
			return hctx.Err()
		})
		if err != nil {
			cancelSub()
			t.Fatalf("Subscribe %d: %v", i, err)
		}
		var d requeueDelivery
		select {
		case d = <-got:
		case <-time.After(Timeout):
			cancelSub()
			stop()
			t.Fatalf("stop %d: the task was never delivered (lost by the requeue?)", i)
		}
		if d.attempt != 1 {
			t.Errorf("stop %d: delivered as attempt %d; a requeue spends no delivery", i, d.attempt)
		}
		if string(d.env.Data) != `{"k":"v"}` || d.env.Key != "default/contract" || d.env.Schema != "index.RssTask.v1" ||
			d.env.Headers["X-Contract"] != "yes" {
			t.Errorf("stop %d: the copy lost its envelope: %+v", i, d.env)
		}
		if i > 0 && !strings.HasPrefix(d.env.ID, "task-requeue/requeue-") {
			t.Errorf("stop %d: the copy's ID is %q, want task-requeue/requeue-<seq>", i, d.env.ID)
		}
		if i > 0 && d.env.ID == lastID {
			t.Errorf("stop %d: the copy kept the ID %q of the delivery it replaced", i, d.env.ID)
		}
		lastID = d.env.ID
		begun := time.Now()
		cancelSub()
		stop()
		if took := time.Since(begun); took >= sub.Drain {
			t.Errorf("stop %d took %v: it waited out its %v drain instead of cancelling the handler", i, took, sub.Drain)
		}
		select {
		case c := <-cause:
			if !errors.Is(c, events.ErrStopping) {
				t.Errorf("stop %d: the handler's context ended with cause %v, want ErrStopping", i, c)
			}
		default:
			t.Errorf("stop %d: the handler had not returned when the stop did", i)
		}
	}
	done := make(chan uint64, 1)
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		done <- m.Attempt()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	select {
	case a := <-done:
		if a != 1 {
			t.Errorf("the finishing delivery is attempt %d, want 1", a)
		}
	case <-time.After(Timeout):
		t.Fatal("the requeued task was never delivered to the subscriber that finishes it")
	}
	waitUntil(t, "the requeued task's acknowledgement", func() bool {
		subjects, err := sa.Subjects(ctx, sub.Stream, subject)
		return err == nil && len(subjects) == 0
	})
	time.Sleep(300 * time.Millisecond)
	if n := dlq.len(); n != 0 {
		e, _ := dlq.at(0)
		t.Errorf("%d dead letters; a requeue is never one (reason %q)", n, e.Headers[events.HeaderDLQReason])
	}
}

// testRequeueKeepsAFinishedOrRefusedResult: a handler a stop cancelled that
// still returns nil has finished, and is acknowledged; one that returns a
// Discard is dead-lettered, not handed back.
func testRequeueKeepsAFinishedOrRefusedResult(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)
	sa := admin(t, bus)
	sub := requeueSub("ct-requeue-results")
	Bind(ctx, t, bus, Topology(), sub)
	for _, tc := range []struct {
		name   string
		result error
		dlq    int
	}{
		{"finished", nil, 0},
		{"refused", events.Discard("poison", nil), 1},
	} {
		subject := events.WorkRSSSubject("idx-" + tc.name)
		if _, err := bus.Publish(ctx, subject, envelope("task-"+tc.name, "index.RssTask.v1", map[string]string{"k": tc.name})); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		subCtx, cancelSub := context.WithCancel(ctx)
		started := make(chan struct{}, 1)
		stop, err := bus.Subscribe(subCtx, sub, func(hctx context.Context, _ events.Message) error {
			started <- struct{}{}
			<-hctx.Done()
			return tc.result
		})
		if err != nil {
			cancelSub()
			t.Fatalf("Subscribe %s: %v", tc.name, err)
		}
		select {
		case <-started:
		case <-time.After(Timeout):
			t.Fatalf("%s: never delivered", tc.name)
		}
		cancelSub()
		stop()
		waitUntil(t, tc.name+"'s settlement", func() bool {
			subjects, err := sa.Subjects(ctx, sub.Stream, subject)
			return err == nil && len(subjects) == 0
		})
		if tc.dlq > 0 {
			dlq.waitFor(t, tc.dlq, "dead letters")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := dlq.len(); n != 1 {
		t.Errorf("%d dead letters, want only the refused task's", n)
	}
}
