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
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestSubscribeRebindsAfterItsStreamIsDeleted is the NATS-restart case the
// contract suite cannot stage (StreamAdmin deletes durables, not streams): a
// single-node restart wipes a memory-backed stream and its consumers, the
// manager re-ensures, and a running subscription resumes on its own (spec §5.9).
//
// The first task holds the subscription's one slot while the stream is
// deleted, so its next fetch is made against the missing durable and the pull
// loop must wait for it and bind it again: a pull already open when a durable
// is re-created under the same name is served by the new one, which would
// hide a loop that gives up.
func TestSubscribeRebindsAfterItsStreamIsDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	top := contracttest.Topology()
	if err := bus.Ensure(ctx, top); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	sub := events.Subscription{
		Stream: events.StreamWorkCaption, Durable: "nb-rebind-stream",
		Filters: []string{events.FilterCaptionFetch},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1,
	}
	contracttest.Bind(ctx, t, bus, top, sub)
	got := make(chan string, 4)
	release := make(chan struct{})
	stop, err := bus.Subscribe(ctx, sub, func(_ context.Context, m events.Message) error {
		got <- m.Envelope().ID
		if m.Envelope().ID == "before" {
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stop()
	publish := func(id string) {
		t.Helper()
		if _, err := bus.Publish(ctx, events.WorkFetchSubject(events.PriorityNormal, id, "en"),
			&events.Envelope{ID: id, Data: []byte(`{}`)}); err != nil {
			t.Fatalf("Publish %s: %v", id, err)
		}
	}
	wait := func(id string) {
		t.Helper()
		select {
		case g := <-got:
			if g != id {
				t.Fatalf("handled %s, want %s", g, id)
			}
		case <-time.After(contracttest.Timeout):
			t.Fatalf("%s was never handled", id)
		}
	}
	publish("before")
	wait("before")
	if err := bus.JetStream().DeleteStream(ctx, events.StreamWorkCaption); err != nil {
		t.Fatalf("delete stream: %v", err)
	}
	close(release) // the slot frees; the next fetch finds no durable
	time.Sleep(time.Second)
	contracttest.Bind(ctx, t, bus, top, sub) // the manager re-ensures
	publish("after")
	wait("after")
}
