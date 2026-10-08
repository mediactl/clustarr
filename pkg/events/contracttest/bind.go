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
	"slices"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// Bind ensures base plus one durable per sub, each with its dead-letter
// watcher, the way the manager's EnsureTopology creates a static consumer, so
// a Subscribe that binds and never creates (spec §5.9) finds it. A test
// subscribes only to durables a topology declares: base's own, or the ones it
// binds here. Each sub must set MaxDeliver above len(Backoff), as every
// topology consumer must (Topology.Validate): a Subscription's zero
// MaxDeliver means unlimited, which only a dead-letter watcher may be. A sub
// named like one of base's consumers replaces it, so a test may re-size a
// durable the production topology declares.
//
// base must be the topology the test already ensured (Ensure replaces the
// bus's remembered topology, and natsbus refuses a replica change on a
// running server), so a test on events.Default().ForSingleNode() passes that.
// Bind is the one cure for a test that would otherwise hang in Subscribe
// waiting for a durable nothing declares (W4.42, W4.45).
func Bind(ctx context.Context, t testing.TB, bus events.Bus, base events.Topology, subs ...events.Subscription) {
	t.Helper()
	if err := bus.Ensure(ctx, BindTopology(t, base, subs...)); err != nil {
		t.Fatalf("contracttest.Bind: Ensure: %v", err)
	}
}

// BindTopology is the topology Bind ensures: base, with each sub's durable and
// its dead-letter watcher added (or replacing a consumer base already names).
// A test that ensures through something other than Bus.Ensure -- a manager's
// keeper, a fake -- builds the same layout from it.
func BindTopology(t testing.TB, base events.Topology, subs ...events.Subscription) events.Topology {
	t.Helper()
	top := base
	top.Consumers = append([]events.ConsumerSpec(nil), base.Consumers...)
	for _, s := range subs {
		if s.MaxDeliver <= len(s.Backoff) {
			t.Fatalf("contracttest.Bind: %s: set MaxDeliver above len(Backoff)=%d, got %d",
				s.Durable, len(s.Backoff), s.MaxDeliver)
		}
		c := events.SubscriptionSpec(s)
		w := events.DeadLetterWatcherSpec(c)
		top.Consumers = slices.DeleteFunc(top.Consumers, func(have events.ConsumerSpec) bool {
			return have.Name == c.Name || have.Name == w.Name
		})
		top.Consumers = append(top.Consumers, c, w)
	}
	return top
}

// admin is bus's StreamAdmin half.
func admin(t *testing.T, bus events.Bus) events.StreamAdmin {
	t.Helper()
	sa, ok := bus.(events.StreamAdmin)
	if !ok {
		t.Fatalf("%T does not implement events.StreamAdmin", bus)
	}
	return sa
}

// testBindCreatesTheDurableAndItsWatcher holds Bind to what the manager's
// EnsureTopology creates for a static consumer: the durable on its stream and
// its dead-letter watcher on StreamAdvisories.
func testBindCreatesTheDurableAndItsWatcher(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	sa := admin(t, bus)
	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "ct-bind",
		Filters: []string{events.FilterIndexRSS},
		AckWait: time.Second, MaxDeliver: 2, MaxInFlight: 1,
	}
	Bind(ctx, t, bus, Topology(), sub)

	durables, err := sa.Subscriptions(ctx, events.StreamWorkIndex)
	if err != nil {
		t.Fatalf("Subscriptions: %v", err)
	}
	if !slices.Contains(durables, sub.Durable) {
		t.Errorf("Subscriptions(%s) = %v, want %s", events.StreamWorkIndex, durables, sub.Durable)
	}
	watchers, err := sa.Subscriptions(ctx, events.StreamAdvisories)
	if err != nil {
		t.Fatalf("Subscriptions advisories: %v", err)
	}
	if want := events.DeadLetterWatcherName(sub.Stream, sub.Durable); !slices.Contains(watchers, want) {
		t.Errorf("Subscriptions(%s) = %v, want %s", events.StreamAdvisories, watchers, want)
	}
}
