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

package agent

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

// domainBus gives every subscription of the domain its drain (§3.5.6) and
// applies CLUSTARR_CONSUMER_SLOTS (§9.2), so the registration packages keep
// subscribing exactly as they did.
//
// Embedding the bus hides any optional interface the concrete bus has
// beyond events.Bus. The one agent code asserts is the events domain's DLQ
// reader (history.DLQReaderFor wants JetStream()), so domainBus forwards it;
// on a bus without one it returns nil, which DLQReaderFor reads as "no
// JetStream" exactly as before.
type domainBus struct {
	events.Bus
	drain time.Duration
	slots map[string]int
}

// Subscribe applies the domain's drain to a subscription that names none,
// and the slot override for its durable.
func (b domainBus) Subscribe(ctx context.Context, s events.Subscription, h events.Handler) (func(), error) {
	if s.Drain == 0 {
		s.Drain = b.drain
	}
	if n, ok := b.slots[s.Durable]; ok {
		s.MaxInFlight = n
	}
	return b.Bus.Subscribe(ctx, s, h)
}

// JetStream forwards the underlying bus's JetStream context, or nil.
func (b domainBus) JetStream() jetstream.JetStream {
	if js, ok := b.Bus.(interface{ JetStream() jetstream.JetStream }); ok {
		return js.JetStream()
	}
	return nil
}
