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
	"testing"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func TestBusContract(t *testing.T) {
	contracttest.RunBusContract(t, func() events.Bus { return membus.New(nil) })
}

func TestPullContract(t *testing.T) {
	contracttest.RunPullContract(t, func() events.Bus { return membus.New(nil) })
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
