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

// Package contracttest holds the conformance suite that every events.Bus
// implementation must pass.
//
// It is an exported test helper rather than an internal test so that natsbus,
// membus and any future implementation are all held to one definition of
// correct behaviour: if membus and natsbus disagree, a unit test written
// against membus is worthless.
package contracttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// Timeout bounds every wait in the suite. It is generous because the
// JetStream run starts a real server.
const Timeout = 30 * time.Second

// Topology is the layout the suite applies. It is the production topology
// shrunk to a single node, with the durable consumers removed: the suite
// creates its own consumers with test-sized ack waits and backoffs, and
// JetStream refuses two consumers with overlapping filters on the same
// work-queue stream.
func Topology() events.Topology {
	t := events.Default().ForSingleNode()
	t.Consumers = nil
	return t
}

// RunBusContract runs the whole suite against the bus returned by newBus.
// newBus is called once per subtest and must return a bus that has not yet
// had Ensure called on it; the suite closes each bus when its subtest ends.
func RunBusContract(t *testing.T, newBus func() events.Bus) {
	t.Helper()
	t.Run("PublishSubscribe", func(t *testing.T) { testPublishSubscribe(t, newBus) })
	t.Run("Deduplication", func(t *testing.T) { testDeduplication(t, newBus) })
	t.Run("WorkQueueRetryThenAck", func(t *testing.T) { testRetryThenAck(t, newBus) })
	t.Run("WorkQueueDiscardToDLQ", func(t *testing.T) { testDiscardToDLQ(t, newBus) })
	t.Run("WorkQueueMaxDeliverToDLQ", func(t *testing.T) { testMaxDeliverToDLQ(t, newBus) })
	t.Run("WorkQueueAckRemoves", func(t *testing.T) { testAckRemoves(t, newBus) })
	t.Run("ScheduledPublish", func(t *testing.T) { testScheduledPublish(t, newBus) })
	t.Run("KeyValueCreateAndCAS", func(t *testing.T) { testKVCreateAndCAS(t, newBus) })
	t.Run("KeyValueDeleteRevision", func(t *testing.T) { testKVDeleteRevision(t, newBus) })
	t.Run("KeyValueTTL", func(t *testing.T) { testKVTTL(t, newBus) })
	t.Run("KeyValueWatch", func(t *testing.T) { testKVWatch(t, newBus) })
	t.Run("RequestReply", func(t *testing.T) { testRequestReply(t, newBus) })
	t.Run("UnknownSubject", func(t *testing.T) { testUnknownSubject(t, newBus) })
}

// Run is the name the design document gives the suite entry point. It is an
// alias for RunBusContract.
func Run(t *testing.T, newBus func() events.Bus) {
	t.Helper()
	RunBusContract(t, newBus)
}

// RunHooksContract runs the publish/receive observability-hooks conformance
// case against a bus built with no hooks (newBus, reusing RunBusContract's
// constructor) and a bus built with hooks installed (newBusWithHooks). It is
// a separate entry point from RunBusContract because installing hooks
// changes the bus's construction, not just its topology: natsbus and membus
// each accept events.Hooks through a constructor Option.
func RunHooksContract(t *testing.T, newBus func() events.Bus,
	newBusWithHooks func(events.Hooks) events.Bus,
) {
	t.Helper()
	t.Run("Hooks", func(t *testing.T) { testHooksContract(t, newBus, newBusWithHooks) })
}

// setup builds a bus with the contract topology applied and registers its
// shutdown.
func setup(t *testing.T, newBus func() events.Bus) (context.Context, events.Bus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	t.Cleanup(cancel)
	bus := newBus()
	t.Cleanup(func() {
		if err := bus.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := bus.Ensure(ctx, Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return ctx, bus
}

// collector accumulates deliveries for assertions.
type collector struct {
	mu   sync.Mutex
	msgs []*events.Envelope
	subs []string
	ch   chan struct{}
}

func newCollector() *collector {
	return &collector{ch: make(chan struct{}, 256)}
}

func (c *collector) add(m events.Message) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m.Envelope())
	c.subs = append(c.subs, m.Subject())
	c.mu.Unlock()
	select {
	case c.ch <- struct{}{}:
	default:
	}
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func (c *collector) at(i int) (*events.Envelope, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msgs[i], c.subs[i]
}

// waitFor blocks until want deliveries have arrived.
func (c *collector) waitFor(t *testing.T, want int, what string) {
	t.Helper()
	deadline := time.After(Timeout)
	for c.len() < want {
		select {
		case <-c.ch:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %d %s, got %d", want, what, c.len())
		}
	}
}

// waitUntil polls cond until it holds or the suite timeout expires.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(Timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// envelope builds a test envelope with a JSON body.
func envelope(id, schema string, body any) *events.Envelope {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return &events.Envelope{
		ID:      id,
		Type:    "contracttest",
		Schema:  schema,
		Source:  "contracttest@0.0.0",
		Key:     "default/contract",
		Trace:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Headers: map[string]string{"X-Contract": "yes"},
		Data:    data,
	}
}

func testPublishSubscribe(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	got := newCollector()

	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamEvents,
		Durable:     "ct-events",
		Filters:     []string{events.FilterAllEvents},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 8,
	}, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	subject := events.CatalogItemSubject("movie", events.ActionAdded, "uid-1")
	env := envelope("evt-1", "catalog.ItemEvent.v1", map[string]string{"title": "Arrival"})
	rec, err := bus.Publish(ctx, subject, env)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rec.Stream != events.StreamEvents {
		t.Errorf("Receipt.Stream = %q, want %q", rec.Stream, events.StreamEvents)
	}
	if rec.Seq == 0 {
		t.Error("Receipt.Seq = 0, want a stream sequence")
	}
	if rec.Duplicate {
		t.Error("Receipt.Duplicate = true on a first publish")
	}

	got.waitFor(t, 1, "deliveries")
	e, sub := got.at(0)
	if sub != subject {
		t.Errorf("Subject = %q, want %q", sub, subject)
	}
	if e.ID != "evt-1" {
		t.Errorf("Envelope.ID = %q, want %q", e.ID, "evt-1")
	}
	if e.Schema != "catalog.ItemEvent.v1" {
		t.Errorf("Envelope.Schema = %q, want %q", e.Schema, "catalog.ItemEvent.v1")
	}
	if e.Source != "contracttest@0.0.0" {
		t.Errorf("Envelope.Source = %q", e.Source)
	}
	if e.Key != "default/contract" {
		t.Errorf("Envelope.Key = %q", e.Key)
	}
	if e.Trace == "" {
		t.Error("Envelope.Trace was not propagated")
	}
	if e.Time.IsZero() {
		t.Error("Envelope.Time was not propagated")
	}
	if e.Headers["X-Contract"] != "yes" {
		t.Errorf("custom header lost: %v", e.Headers)
	}
	if string(e.Data) != string(env.Data) {
		t.Errorf("Data = %q, want %q", e.Data, env.Data)
	}
}

func testDeduplication(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	subject := events.CatalogItemSubject("movie", events.ActionUpdated, "uid-dedup")
	id := events.MsgIDForObject("uid-dedup", 7, "item")

	first, err := bus.Publish(ctx, subject, envelope(id, "catalog.ItemEvent.v1", 1))
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if first.Duplicate {
		t.Fatal("first publish reported as duplicate")
	}
	second, err := bus.Publish(ctx, subject, envelope(id, "catalog.ItemEvent.v1", 2))
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if !second.Duplicate {
		t.Error("second publish of the same Nats-Msg-Id was not reported as a duplicate")
	}
	if second.Seq != first.Seq {
		t.Errorf("duplicate Seq = %d, want the original %d", second.Seq, first.Seq)
	}
}

// subscribeDLQ attaches a collector to the dead-letter stream.
func subscribeDLQ(ctx context.Context, t *testing.T, bus events.Bus) *collector {
	t.Helper()
	got := newCollector()
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamDLQ,
		Durable:     "ct-dlq",
		Filters:     []string{events.FilterAllDLQ},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 8,
	}, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe DLQ: %v", err)
	}
	t.Cleanup(stop)
	return got
}

func testRetryThenAck(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)

	var mu sync.Mutex
	var attempts []uint64
	done := make(chan struct{})

	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamWorkCatalogarr,
		Durable:     "ct-search-high",
		Filters:     []string{"clustarr.work.catalogarr.search.high.>"},
		AckWait:     5 * time.Second,
		MaxDeliver:  4,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 1,
	}, func(ctx context.Context, m events.Message) error {
		mu.Lock()
		attempts = append(attempts, m.Attempt())
		n := len(attempts)
		mu.Unlock()
		if n == 1 {
			// Exercise the heartbeat before asking for a redelivery.
			if err := m.InProgress(ctx); err != nil {
				t.Errorf("InProgress: %v", err)
			}
			return events.Retry(50*time.Millisecond, errors.New("indexer busy"))
		}
		close(done)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	subject := events.WorkSearchSubject(events.PriorityHigh, "movie-1")
	if _, err := bus.Publish(ctx, subject,
		envelope("task-retry", "catalog.SearchTask.v1", map[string]string{"k": "v"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(Timeout):
		t.Fatal("task was never redelivered and acknowledged")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %v, want exactly two deliveries", attempts)
	}
	if attempts[0] != 1 || attempts[1] != 2 {
		t.Errorf("Attempt() = %v, want [1 2]", attempts)
	}
	if n := dlq.len(); n != 0 {
		t.Errorf("dead-lettered %d messages, want 0", n)
	}
}

func testDiscardToDLQ(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)

	var mu sync.Mutex
	deliveries := 0
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamWorkCatalogarr,
		Durable:     "ct-import",
		Filters:     []string{events.FilterCatalogImport},
		AckWait:     5 * time.Second,
		MaxDeliver:  5,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 1,
	}, func(_ context.Context, _ events.Message) error {
		mu.Lock()
		deliveries++
		mu.Unlock()
		return events.Discard("unparsable payload", errors.New("bad json"))
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	subject := events.WorkImportSubject("dl-1")
	body := envelope("task-discard", "catalog.ImportTask.v1", map[string]string{"k": "v"})
	if _, err := bus.Publish(ctx, subject, body); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	dlq.waitFor(t, 1, "dead-letter copies")
	e, dlqSubject := dlq.at(0)
	if want := events.DLQSubject("catalogarr", "import", "task-discard"); dlqSubject != want {
		t.Errorf("DLQ subject = %q, want %q", dlqSubject, want)
	}
	if got := e.Headers[events.HeaderDLQReason]; got != "unparsable payload" {
		t.Errorf("%s = %q, want %q", events.HeaderDLQReason, got, "unparsable payload")
	}
	if got := e.Headers[events.HeaderDLQSubject]; got != subject {
		t.Errorf("%s = %q, want %q", events.HeaderDLQSubject, got, subject)
	}
	if got := e.Headers[events.HeaderDLQConsumer]; got != "ct-import" {
		t.Errorf("%s = %q, want %q", events.HeaderDLQConsumer, got, "ct-import")
	}
	if got := e.Headers[events.HeaderDLQAttempts]; got != "1" {
		t.Errorf("%s = %q, want %q", events.HeaderDLQAttempts, got, "1")
	}
	if string(e.Data) != string(body.Data) {
		t.Errorf("dead-lettered payload = %q, want the original %q", e.Data, body.Data)
	}
	if e.Schema != body.Schema {
		t.Errorf("dead-lettered schema = %q, want %q", e.Schema, body.Schema)
	}

	// A discard bypasses MaxDeliver: the task is handled exactly once.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if deliveries != 1 {
		t.Errorf("handler ran %d times, want 1", deliveries)
	}
}

func testMaxDeliverToDLQ(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	dlq := subscribeDLQ(ctx, t, bus)

	var mu sync.Mutex
	var attempts []uint64
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamWorkIndexarr,
		Durable:     "ct-rss",
		Filters:     []string{events.FilterIndexRSS},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{20 * time.Millisecond, 20 * time.Millisecond},
		MaxInFlight: 1,
	}, func(_ context.Context, m events.Message) error {
		mu.Lock()
		attempts = append(attempts, m.Attempt())
		mu.Unlock()
		return errors.New("indexer unreachable")
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-1"),
		envelope("task-rss", "index.RssTask.v1", map[string]string{"k": "v"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	dlq.waitFor(t, 1, "dead-letter copies")
	e, _ := dlq.at(0)
	if got := e.Headers[events.HeaderDLQAttempts]; got != "3" {
		t.Errorf("%s = %q, want %q", events.HeaderDLQAttempts, got, "3")
	}
	if e.Headers[events.HeaderDLQReason] == "" {
		t.Errorf("%s is empty", events.HeaderDLQReason)
	}

	// The delivery budget must be exactly MaxDeliver, not one more.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("attempts = %v, want three deliveries", attempts)
	}
	for i, a := range attempts {
		if a != uint64(i+1) {
			t.Errorf("attempt %d reported Attempt() = %d", i, a)
		}
	}
}

func testAckRemoves(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	got := newCollector()

	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamWorkCaptionarr,
		Durable:     "ct-fetch",
		Filters:     []string{events.FilterCaptionFetch},
		AckWait:     2 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 4,
	}, func(_ context.Context, m events.Message) error {
		got.add(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	for i := range 3 {
		subject := events.WorkFetchSubject(events.PriorityNormal,
			fmt.Sprintf("req-%d", i), "en")
		if _, err := bus.Publish(ctx, subject,
			envelope(fmt.Sprintf("fetch-%d", i), "subtitle.FetchTask.v1",
				map[string]int{"i": i})); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	got.waitFor(t, 3, "work-queue deliveries")

	// An acknowledged work-queue message is gone: nothing is redelivered
	// once the ack wait has passed.
	time.Sleep(2500 * time.Millisecond)
	if n := got.len(); n != 3 {
		t.Errorf("received %d deliveries, want exactly 3", n)
	}
}

// testScheduledPublish covers the delay-profile path: a grab published with
// WithScheduleAt must be held until it is due and then delivered on its real
// work subject, not on the holding subject.
func testScheduledPublish(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)

	type delivery struct {
		at      time.Time
		subject string
	}
	got := make(chan delivery, 4)

	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamWorkCatalogarr,
		Durable:     "ct-grab",
		Filters:     []string{events.FilterCatalogGrab},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 1,
	}, func(_ context.Context, m events.Message) error {
		select {
		case got <- delivery{at: time.Now(), subject: m.Subject()}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()

	subject := events.WorkGrabSubject("movie-delayed")
	due := time.Now().Add(2 * time.Second)
	if _, err := bus.Publish(ctx, subject,
		envelope("grab-delayed", "catalog.GrabTask.v1", map[string]string{"k": "v"}),
		events.WithScheduleAt(due)); err != nil {
		t.Fatalf("Publish with a schedule: %v", err)
	}

	select {
	case d := <-got:
		// Brokers round schedules to their own tick, so allow a little slack
		// before the nominal due time but none of the seconds-scale kind.
		if d.at.Before(due.Add(-1500 * time.Millisecond)) {
			t.Errorf("scheduled task delivered at %s, well before its due time %s",
				d.at, due)
		}
		if d.subject != subject {
			t.Errorf("delivered on %q, want the target subject %q", d.subject, subject)
		}
	case <-time.After(Timeout):
		t.Fatal("scheduled task was never delivered")
	}
}

func testKVCreateAndCAS(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketLeases)
	key := events.LeaseKey("movie/the-thing")

	rev, err := kv.Create(ctx, key, []byte("download-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rev == 0 {
		t.Error("Create returned revision 0")
	}

	if _, err := kv.Create(ctx, key, []byte("download-2")); !errors.Is(err, events.ErrKeyExists) {
		t.Fatalf("second Create error = %v, want ErrKeyExists", err)
	}

	entry, err := kv.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(entry.Value) != "download-1" {
		t.Errorf("Value = %q, want %q", entry.Value, "download-1")
	}
	if entry.Revision != rev {
		t.Errorf("Revision = %d, want %d", entry.Revision, rev)
	}
	if entry.Operation != events.KVPut {
		t.Errorf("Operation = %q, want %q", entry.Operation, events.KVPut)
	}

	if _, err := kv.Update(ctx, key, []byte("download-3"), rev+99); !errors.Is(err, events.ErrRevisionMismatch) {
		t.Fatalf("stale Update error = %v, want ErrRevisionMismatch", err)
	}

	next, err := kv.Update(ctx, key, []byte("download-3"), rev)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if next <= rev {
		t.Errorf("Update revision = %d, want greater than %d", next, rev)
	}

	if err := kv.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kv.Get(ctx, key); !errors.Is(err, events.ErrKeyNotFound) {
		t.Fatalf("Get after Delete error = %v, want ErrKeyNotFound", err)
	}
	// Deleting an absent key is a no-op, so a sweeper can run unconditionally.
	if err := kv.Delete(ctx, key); err != nil {
		t.Errorf("Delete of an absent key: %v", err)
	}
	// A deleted lease can be taken again.
	if _, err := kv.Create(ctx, key, []byte("download-4")); err != nil {
		t.Errorf("Create after Delete: %v", err)
	}
}

// testKVDeleteRevision holds the revision-checked delete: a caller that read
// a key and then decides to delete it must not delete a value another writer
// put there in between -- the grab lease a failed Download frees can be
// reclaimed by a new grab between the read and the delete.
func testKVDeleteRevision(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketLeases)
	key := events.LeaseKey("movie/the-thing")

	read, err := kv.Create(ctx, key, []byte("download-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Another writer moves the key on after it was read.
	moved, err := kv.Update(ctx, key, []byte("download-2"), read)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := kv.DeleteRevision(ctx, key, read); !errors.Is(err, events.ErrRevisionMismatch) {
		t.Fatalf("DeleteRevision at the stale revision error = %v, want ErrRevisionMismatch", err)
	}
	entry, err := kv.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after a refused DeleteRevision: %v", err)
	}
	if string(entry.Value) != "download-2" || entry.Revision != moved {
		t.Errorf("after a refused DeleteRevision the key is %q@%d, want the other writer's %q@%d",
			entry.Value, entry.Revision, "download-2", moved)
	}
	if err := kv.DeleteRevision(ctx, key, 0); !errors.Is(err, events.ErrRevisionMismatch) {
		t.Errorf("DeleteRevision at revision 0 error = %v, want ErrRevisionMismatch, not an unconditional delete", err)
	}

	if err := kv.DeleteRevision(ctx, key, moved); err != nil {
		t.Fatalf("DeleteRevision at the current revision: %v", err)
	}
	if _, err := kv.Get(ctx, key); !errors.Is(err, events.ErrKeyNotFound) {
		t.Fatalf("Get after DeleteRevision error = %v, want ErrKeyNotFound", err)
	}
	// Gone since it was read is a mismatch too, unlike Delete's no-op.
	if err := kv.DeleteRevision(ctx, key, moved); !errors.Is(err, events.ErrRevisionMismatch) {
		t.Errorf("DeleteRevision of a deleted key error = %v, want ErrRevisionMismatch", err)
	}
	if _, err := kv.Create(ctx, key, []byte("download-3")); err != nil {
		t.Errorf("Create after DeleteRevision: %v", err)
	}
}

func testKVTTL(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketSearchCache)
	key := "idx-1.abc123"

	if _, err := kv.Create(ctx, key, []byte("cached"), events.WithTTL(time.Second)); err != nil {
		t.Fatalf("Create with TTL: %v", err)
	}
	if _, err := kv.Get(ctx, key); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	waitUntil(t, "the key to expire", func() bool {
		_, err := kv.Get(ctx, key)
		return errors.Is(err, events.ErrKeyNotFound)
	})
	// Once expired the key may be created again.
	if _, err := kv.Create(ctx, key, []byte("cached again")); err != nil {
		t.Errorf("Create after expiry: %v", err)
	}
}

func testKVWatch(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketProgress)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates, err := kv.Watch(watchCtx, "download.>")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if _, err := kv.Put(ctx, "download.uid-1", []byte(`{"percentMilli":42000}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A key outside the pattern must not be delivered.
	if _, err := kv.Put(ctx, "transcode.uid-2", []byte(`{"percentMilli":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	deadline := time.After(Timeout)
	for {
		select {
		case e, ok := <-updates:
			if !ok {
				t.Fatal("watch channel closed before the update arrived")
			}
			if e.Key == "transcode.uid-2" {
				t.Fatalf("watcher on %q received %q", "download.>", e.Key)
			}
			if e.Key != "download.uid-1" {
				continue
			}
			if string(e.Value) != `{"percentMilli":42000}` {
				t.Errorf("watched value = %q", e.Value)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for a watch update")
		}
	}
}

func testRequestReply(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)

	type request struct {
		Text string `json:"text"`
	}
	type response struct {
		Releases int    `json:"releases"`
		Echo     string `json:"echo"`
	}

	err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndexarr,
		func(_ context.Context, data []byte) ([]byte, error) {
			var in request
			if err := json.Unmarshal(data, &in); err != nil {
				return nil, err
			}
			return json.Marshal(response{Releases: 3, Echo: in.Text})
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	var out response
	if err := bus.Request(ctx, events.RPCIndexSearch, request{Text: "arrival 2016"}, &out); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if out.Releases != 3 || out.Echo != "arrival 2016" {
		t.Errorf("response = %+v", out)
	}

	unserved := events.RPCMetadataResolve
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = bus.Request(reqCtx, unserved, request{Text: "x"}, &out)
	if !errors.Is(err, events.ErrNoResponders) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Request to an unserved subject error = %v, want ErrNoResponders", err)
	}
}

func testUnknownSubject(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	_, err := bus.Publish(ctx, "clustarr.nope.nothing.here",
		envelope("x", "none.v1", map[string]string{}))
	if err == nil {
		t.Fatal("Publish to a subject with no stream succeeded")
	}
}

// hooksCtxKey is the context key the hook cases' synthetic AfterReceive hook
// uses to hand the extracted trace to the handler. It is local to this test
// file, not events.HeaderTrace or pkg/obs/tracing, because pkg/events must
// not import pkg/obs and this suite proves the bus calls the hooks correctly
// without depending on what a real observability hook does with them.
type hooksCtxKey struct{}

// hookDelivery is what a hook-case handler reports back about one delivery:
// the trace carried by the wire envelope it received, and what -- if
// anything -- the AfterReceive hook stashed on its context.
type hookDelivery struct {
	envTrace string
	ctxTrace string
	ctxSet   bool
}

// subscribeForHooks subscribes a handler that reports each delivery's
// envelope trace and hook-stamped context value to the returned channel.
func subscribeForHooks(t *testing.T, ctx context.Context, bus events.Bus, durable string) chan hookDelivery {
	t.Helper()
	got := make(chan hookDelivery, 1)
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      events.StreamEvents,
		Durable:     durable,
		Filters:     []string{events.FilterAllEvents},
		AckWait:     5 * time.Second,
		MaxDeliver:  3,
		Backoff:     []time.Duration{50 * time.Millisecond},
		MaxInFlight: 4,
	}, func(ctx context.Context, m events.Message) error {
		v, ok := ctx.Value(hooksCtxKey{}).(string)
		select {
		case got <- hookDelivery{envTrace: m.Envelope().Trace, ctxTrace: v, ctxSet: ok}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(stop)
	return got
}

// testHooksContract covers Task C1: a BeforePublish hook must stamp the
// outbound envelope before it is encoded onto the wire, an AfterReceive hook
// must run before the handler and its returned context must be what the
// handler receives, and with no hooks installed neither must happen and
// nothing must break.
func testHooksContract(t *testing.T, newBus func() events.Bus,
	newBusWithHooks func(events.Hooks) events.Bus,
) {
	const wantTrace = "00-11112222333344445555666677778888-9999aaaabbbbcccc-01"

	t.Run("HooksInstalledStampAndPropagate", func(t *testing.T) {
		hooks := events.Hooks{
			BeforePublish: func(_ context.Context, e *events.Envelope) {
				e.Trace = wantTrace
			},
			AfterReceive: func(ctx context.Context, e *events.Envelope) context.Context {
				return context.WithValue(ctx, hooksCtxKey{}, e.Trace)
			},
		}
		ctx, bus := setup(t, func() events.Bus { return newBusWithHooks(hooks) })
		got := subscribeForHooks(t, ctx, bus, "ct-hooks-on")

		subject := events.CatalogItemSubject("movie", events.ActionAdded, "uid-hooks-on")
		env := envelope("evt-hooks-on", "catalog.ItemEvent.v1", map[string]string{"k": "v"})
		env.Trace = "" // the hook, not this field, must be what stamps it
		if _, err := bus.Publish(ctx, subject, env); err != nil {
			t.Fatalf("Publish: %v", err)
		}

		select {
		case d := <-got:
			if d.envTrace != wantTrace {
				t.Errorf("delivered Envelope.Trace = %q, want %q (BeforePublish should have stamped it "+
					"before encoding)", d.envTrace, wantTrace)
			}
			if !d.ctxSet || d.ctxTrace != wantTrace {
				t.Errorf("handler context trace = (set=%v) %q, want %q from AfterReceive",
					d.ctxSet, d.ctxTrace, wantTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("handler was never invoked")
		}
	})

	t.Run("NoHooksIsANoOp", func(t *testing.T) {
		ctx, bus := setup(t, newBus)
		got := subscribeForHooks(t, ctx, bus, "ct-hooks-off")

		subject := events.CatalogItemSubject("movie", events.ActionAdded, "uid-hooks-off")
		env := envelope("evt-hooks-off", "catalog.ItemEvent.v1", map[string]string{"k": "v"})
		env.Trace = ""
		if _, err := bus.Publish(ctx, subject, env); err != nil {
			t.Fatalf("Publish: %v", err)
		}

		select {
		case d := <-got:
			if d.envTrace != "" {
				t.Errorf("delivered Envelope.Trace = %q, want empty with no hooks installed", d.envTrace)
			}
			if d.ctxSet {
				t.Errorf("handler context carried the hook's key (%q) with no hooks installed", d.ctxTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("handler was never invoked")
		}
	})

	// The two cases below hold each hook to firing independently: a nil
	// BeforePublish must not stop AfterReceive from running, and a nil
	// AfterReceive must not stop BeforePublish from stamping the wire
	// envelope. Both RunBeforePublish and RunAfterReceive nil-check their
	// own field only, so this is a low-risk regression lock, not a case
	// expected to find a new bug.

	t.Run("OnlyBeforePublishSet", func(t *testing.T) {
		hooks := events.Hooks{
			BeforePublish: func(_ context.Context, e *events.Envelope) {
				e.Trace = wantTrace
			},
			// AfterReceive is deliberately nil.
		}
		ctx, bus := setup(t, func() events.Bus { return newBusWithHooks(hooks) })
		got := subscribeForHooks(t, ctx, bus, "ct-hooks-before-only")

		subject := events.CatalogItemSubject("movie", events.ActionAdded, "uid-hooks-before-only")
		env := envelope("evt-hooks-before-only", "catalog.ItemEvent.v1", map[string]string{"k": "v"})
		env.Trace = ""
		if _, err := bus.Publish(ctx, subject, env); err != nil {
			t.Fatalf("Publish: %v", err)
		}

		select {
		case d := <-got:
			if d.envTrace != wantTrace {
				t.Errorf("delivered Envelope.Trace = %q, want %q (BeforePublish alone should still "+
					"stamp it)", d.envTrace, wantTrace)
			}
			if d.ctxSet {
				t.Errorf("handler context carried the hook's key (%q) with AfterReceive nil", d.ctxTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("handler was never invoked")
		}
	})

	t.Run("OnlyAfterReceiveSet", func(t *testing.T) {
		hooks := events.Hooks{
			// BeforePublish is deliberately nil.
			AfterReceive: func(ctx context.Context, e *events.Envelope) context.Context {
				return context.WithValue(ctx, hooksCtxKey{}, e.Trace)
			},
		}
		ctx, bus := setup(t, func() events.Bus { return newBusWithHooks(hooks) })
		got := subscribeForHooks(t, ctx, bus, "ct-hooks-after-only")

		subject := events.CatalogItemSubject("movie", events.ActionAdded, "uid-hooks-after-only")
		env := envelope("evt-hooks-after-only", "catalog.ItemEvent.v1", map[string]string{"k": "v"})
		env.Trace = ""
		if _, err := bus.Publish(ctx, subject, env); err != nil {
			t.Fatalf("Publish: %v", err)
		}

		select {
		case d := <-got:
			if d.envTrace != "" {
				t.Errorf("delivered Envelope.Trace = %q, want empty with BeforePublish nil", d.envTrace)
			}
			if !d.ctxSet || d.ctxTrace != "" {
				t.Errorf("handler context trace = (set=%v) %q, want set=true value=\"\" from "+
					"AfterReceive (it must still run even though BeforePublish is nil)",
					d.ctxSet, d.ctxTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("handler was never invoked")
		}
	})

	// RPC request/reply must be held to the same contract as publish/
	// subscribe: BeforePublish runs on the outbound request (a request IS a
	// publish) and AfterReceive runs on the inbound request before the
	// responder's handler, so a handler's own spans and any outbound calls
	// it makes are children of the caller's trace rather than orphaned
	// roots. The reply leg is deliberately NOT run through the hooks -- see
	// natsbus.Bus.Request's doc comment for why: a request/reply round trip
	// is synchronous from the caller's point of view, so there is no second,
	// independent hop for a hook to bridge on the way back.

	t.Run("RPCRequestReplyPropagatesTrace", func(t *testing.T) {
		hooks := events.Hooks{
			BeforePublish: func(_ context.Context, e *events.Envelope) {
				e.Trace = wantTrace
			},
			AfterReceive: func(ctx context.Context, e *events.Envelope) context.Context {
				return context.WithValue(ctx, hooksCtxKey{}, e.Trace)
			},
		}
		ctx, bus := setup(t, func() events.Bus { return newBusWithHooks(hooks) })
		got := make(chan hookDelivery, 1)

		err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndexarr,
			func(ctx context.Context, _ []byte) ([]byte, error) {
				v, ok := ctx.Value(hooksCtxKey{}).(string)
				select {
				case got <- hookDelivery{ctxTrace: v, ctxSet: ok}:
				default:
				}
				return []byte(`{}`), nil
			})
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}

		var out map[string]any
		if err := bus.Request(ctx, events.RPCIndexSearch, map[string]string{"k": "v"}, &out); err != nil {
			t.Fatalf("Request: %v", err)
		}

		select {
		case d := <-got:
			if !d.ctxSet || d.ctxTrace != wantTrace {
				t.Errorf("RPC handler context trace = (set=%v) %q, want %q from AfterReceive",
					d.ctxSet, d.ctxTrace, wantTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("RPC handler was never invoked")
		}
	})

	t.Run("RPCRequestReplyNoHooksIsANoOp", func(t *testing.T) {
		ctx, bus := setup(t, newBus)
		got := make(chan hookDelivery, 1)

		err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndexarr,
			func(ctx context.Context, _ []byte) ([]byte, error) {
				v, ok := ctx.Value(hooksCtxKey{}).(string)
				select {
				case got <- hookDelivery{ctxTrace: v, ctxSet: ok}:
				default:
				}
				return []byte(`{}`), nil
			})
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}

		var out map[string]any
		if err := bus.Request(ctx, events.RPCIndexSearch, map[string]string{"k": "v"}, &out); err != nil {
			t.Fatalf("Request: %v", err)
		}

		select {
		case d := <-got:
			if d.ctxSet {
				t.Errorf("RPC handler context carried the hook's key (%q) with no hooks installed",
					d.ctxTrace)
			}
		case <-time.After(Timeout):
			t.Fatal("RPC handler was never invoked")
		}
	})
}
