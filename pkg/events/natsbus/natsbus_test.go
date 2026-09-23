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

package natsbus_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// startServer boots an embedded JetStream server with its store under the
// test's temporary directory, so the suite needs no external broker.
func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-contract",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestBusContract(t *testing.T) {
	contracttest.RunBusContract(t, func() events.Bus {
		bus, err := natsbus.New(connect(t))
		if err != nil {
			t.Fatalf("natsbus.New: %v", err)
		}
		return bus
	})
}

func TestPullContract(t *testing.T) {
	contracttest.RunPullContract(t, func() events.Bus {
		bus, err := natsbus.New(connect(t))
		if err != nil {
			t.Fatalf("natsbus.New: %v", err)
		}
		return bus
	})
}

func TestHooksContract(t *testing.T) {
	contracttest.RunHooksContract(t,
		func() events.Bus {
			bus, err := natsbus.New(connect(t))
			if err != nil {
				t.Fatalf("natsbus.New: %v", err)
			}
			return bus
		},
		func(h events.Hooks) events.Bus {
			bus, err := natsbus.New(connect(t), natsbus.WithHooks(h))
			if err != nil {
				t.Fatalf("natsbus.New: %v", err)
			}
			return bus
		},
	)
}

// TestEnsureDefaultTopology applies the production topology, shrunk only to a
// single replica, so every stream, durable consumer and bucket in the design
// is validated against a real server.
func TestEnsureDefaultTopology(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	defer func() { _ = bus.Close() }()

	top := events.Default().ForSingleNode()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Ensure is idempotent: every replica runs it at startup.
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	js := bus.JetStream()
	for _, spec := range top.Streams {
		st, err := js.Stream(ctx, spec.Name)
		if err != nil {
			t.Fatalf("stream %s: %v", spec.Name, err)
		}
		cfg := st.CachedInfo().Config
		if cfg.Retention != events.StreamConfig(spec).Retention {
			t.Errorf("stream %s retention = %v", spec.Name, cfg.Retention)
		}
		if spec.Retention == events.RetentionWorkQueue && !cfg.AllowMsgSchedules &&
			spec.Name != events.StreamAdvisories && spec.Name != events.StreamWorkSquasharr {
			t.Errorf("work stream %s does not allow message schedules", spec.Name)
		}
	}
	for _, spec := range top.Consumers {
		c, err := js.Consumer(ctx, spec.Stream, spec.Name)
		if err != nil {
			t.Fatalf("consumer %s: %v", spec.Name, err)
		}
		cfg := c.CachedInfo().Config
		if cfg.MaxDeliver != spec.MaxDeliver {
			t.Errorf("consumer %s MaxDeliver = %d, want %d",
				spec.Name, cfg.MaxDeliver, spec.MaxDeliver)
		}
		if len(cfg.BackOff) != len(spec.BackOff) {
			t.Errorf("consumer %s BackOff = %v, want %v",
				spec.Name, cfg.BackOff, spec.BackOff)
		}
	}
	for _, spec := range top.Buckets {
		if _, err := js.KeyValue(ctx, spec.Name); err != nil {
			t.Fatalf("bucket %s: %v", spec.Name, err)
		}
	}
}

// TestEnsureRefusesRetentionChange pins the guard that keeps an operator from
// silently dropping queued work by flipping a work stream to Limits.
func TestEnsureRefusesRetentionChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
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
	if err := bus.Ensure(ctx, top); err == nil {
		t.Fatal("Ensure accepted a changed work-stream retention")
	} else if !isRetentionErr(err) {
		t.Fatalf("Ensure error = %v, want ErrRetentionImmutable", err)
	}
}

func isRetentionErr(err error) bool {
	return errors.Is(err, events.ErrRetentionImmutable)
}

// hangConsumer subscribes durable on stream with a handler that blocks until
// the test ends, and returns every MAX_DELIVERIES advisory JetStream
// publishes for it, captured off the wire so a test can re-send one.
func hangConsumer(ctx context.Context, t *testing.T, bus *natsbus.Bus, nc *nats.Conn,
	stream, durable, filter string,
) <-chan *nats.Msg {
	t.Helper()
	advisories := make(chan *nats.Msg, 8)
	adv, err := nc.ChanSubscribe(
		natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre+"."+stream+"."+durable, advisories)
	if err != nil {
		t.Fatalf("subscribe to advisories: %v", err)
	}
	t.Cleanup(func() { _ = adv.Unsubscribe() })

	release := make(chan struct{})
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:      stream,
		Durable:     durable,
		Filters:     []string{filter},
		AckWait:     200 * time.Millisecond,
		MaxDeliver:  2,
		Backoff:     []time.Duration{200 * time.Millisecond},
		MaxInFlight: 3,
	}, func(ctx context.Context, _ events.Message) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("hung")
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Cleanups run last-in first-out: release the hung handler, then stop.
	t.Cleanup(stop)
	t.Cleanup(func() { close(release) })
	return advisories
}

// storedMsgs is how many messages the named stream holds.
func storedMsgs(ctx context.Context, t *testing.T, bus *natsbus.Bus, stream string) uint64 {
	t.Helper()
	st, err := bus.JetStream().Stream(ctx, stream)
	if err != nil {
		t.Fatalf("stream %s: %v", stream, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		t.Fatalf("stream %s info: %v", stream, err)
	}
	return info.State.Msgs
}

func waitForMsgs(ctx context.Context, t *testing.T, bus *natsbus.Bus, stream string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(contracttest.Timeout)
	for storedMsgs(ctx, t, bus, stream) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%s holds %d messages, want %d", stream,
				storedMsgs(ctx, t, bus, stream), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func resend(t *testing.T, nc *nats.Conn, advisories <-chan *nats.Msg) {
	t.Helper()
	select {
	case adv := <-advisories:
		if err := nc.Publish(adv.Subject, adv.Data); err != nil {
			t.Fatalf("re-send advisory: %v", err)
		}
		if err := nc.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	case <-time.After(contracttest.Timeout):
		t.Fatal("JetStream never published a MAX_DELIVERIES advisory")
	}
	time.Sleep(500 * time.Millisecond)
}

// TestLapsedWorkQueueMessageIsDeleted pins the half of the advisory path the
// contract suite cannot observe: once a work-queue message whose final
// delivery lapsed is copied to the DLQ, it is deleted from its work stream,
// as an in-process Term would remove it. JetStream itself leaves it stored
// indefinitely. A re-sent advisory then finds nothing to copy.
func TestLapsedWorkQueueMessageIsDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := connect(t)
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	advisories := hangConsumer(ctx, t, bus, nc,
		events.StreamWorkIndexarr, "nb-hung", events.FilterIndexRSS)
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-1"),
		&events.Envelope{ID: "task-wq", Data: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitForMsgs(ctx, t, bus, events.StreamDLQ, 1)
	waitForMsgs(ctx, t, bus, events.StreamWorkIndexarr, 0)

	resend(t, nc, advisories)
	if n := storedMsgs(ctx, t, bus, events.StreamDLQ); n != 1 {
		t.Errorf("DLQ holds %d copies after a re-sent advisory, want 1", n)
	}
}

// TestResentAdvisoryIsDeduplicated re-sends a real MAX_DELIVERIES advisory
// for a message that is still stored -- on a Limits stream, which the
// watcher never deletes from -- so the watcher reads and copies it a second
// time. The copy's Msg-Id must be stable, so CLUSTARR_DLQ's duplicate window
// absorbs it. A release, like an event, names no task, so the copy must be
// named by the consuming durable, as events.Settle's path names it.
func TestResentAdvisoryIsDeduplicated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := connect(t)
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	advisories := hangConsumer(ctx, t, bus, nc,
		events.StreamReleases, "nb-hung-rel", events.FilterAllReleases)
	if _, err := bus.Publish(ctx, events.ReleaseSubject("torrent", "idx", 2000),
		&events.Envelope{ID: "rel-1", Data: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitForMsgs(ctx, t, bus, events.StreamDLQ, 1)
	if n := storedMsgs(ctx, t, bus, events.StreamReleases); n != 1 {
		t.Errorf("%s holds %d messages, want the lapsed one kept", events.StreamReleases, n)
	}
	// A release subject names no task, so the copy is named by the durable.
	dlq, err := bus.JetStream().Stream(ctx, events.StreamDLQ)
	if err != nil {
		t.Fatalf("DLQ stream: %v", err)
	}
	if _, err := dlq.GetLastMsgForSubject(ctx, events.DLQSubject("nb", "hung-rel", "rel-1")); err != nil {
		t.Errorf("no dead letter on the durable-named subject: %v", err)
	}

	resend(t, nc, advisories)
	if n := storedMsgs(ctx, t, bus, events.StreamDLQ); n != 1 {
		t.Errorf("DLQ holds %d copies after a re-sent advisory, want 1", n)
	}
}

// TestFailedLapseCopyIsRetried pins the other half of reading advisories from
// a stream rather than off the wire: an advisory whose dead-letter copy fails
// is handled again until the copy is stored, where the core-NATS watcher
// logged the failure and dropped the advisory, and with it the only record of
// the message. The DLQ stream is deleted for the first attempt, so the copy
// cannot be stored until Ensure puts it back.
func TestFailedLapseCopyIsRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := connect(t)
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	top := contracttest.Topology()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	advisories := hangConsumer(ctx, t, bus, nc,
		events.StreamWorkIndexarr, "nb-retry", events.FilterIndexRSS)
	if err := bus.JetStream().DeleteStream(ctx, events.StreamDLQ); err != nil {
		t.Fatalf("delete %s: %v", events.StreamDLQ, err)
	}
	if _, err := bus.Publish(ctx, events.WorkRSSSubject("idx-retry"),
		&events.Envelope{ID: "task-retry", Data: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case <-advisories:
	case <-time.After(contracttest.Timeout):
		t.Fatal("JetStream never published a MAX_DELIVERIES advisory")
	}
	// Let the watcher try, and fail, with nowhere to copy to.
	time.Sleep(500 * time.Millisecond)
	if n := storedMsgs(ctx, t, bus, events.StreamWorkIndexarr); n != 1 {
		t.Fatalf("%s holds %d messages before any copy was possible, want the lapsed one", events.StreamWorkIndexarr, n)
	}

	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	waitForMsgs(ctx, t, bus, events.StreamDLQ, 1)
	waitForMsgs(ctx, t, bus, events.StreamWorkIndexarr, 0)
	waitForMsgs(ctx, t, bus, events.StreamAdvisories, 0)
}
