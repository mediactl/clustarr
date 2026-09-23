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

package membus

import (
	"context"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.PullSubscriber = (*Bus)(nil)

// puller hands out one message per Next call from a shared durable. It joins
// the same claim state, deduplication and ack-deadline sweep Subscribe's
// delivery loop uses (claimNext, membus.go), so a message claimed through
// Pull is indistinguishable from one Subscribe would have delivered, but it
// runs no delivery goroutine of its own: Next claims synchronously when the
// caller asks for one, rather than a background loop handing them to a
// handler.
type puller struct {
	bus     *Bus
	stream  *stream
	sub     events.Subscription
	ackWait func(attempt uint64) time.Duration
}

// Pull implements events.PullSubscriber.
func (b *Bus) Pull(_ context.Context, s events.Subscription) (events.Puller, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	st := b.streams[s.Stream]
	b.mu.Unlock()
	if closed {
		return nil, events.ErrClosed
	}
	if st == nil {
		return nil, fmt.Errorf("membus: stream %s not ensured: %w", s.Stream, events.ErrStreamNotFound)
	}
	return &puller{
		bus:     b,
		stream:  st,
		sub:     s,
		ackWait: func(attempt uint64) time.Duration { return ackWaitFor(s, attempt) },
	}, nil
}

// Next implements events.Puller. It polls at the same pollInterval Subscribe
// uses, since membus has no broker to push a wakeup from.
func (p *puller) Next(ctx context.Context) (context.Context, events.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ctx, nil, err
		}
		if p.bus.isClosed() {
			return ctx, nil, events.ErrClosed
		}
		if m := p.bus.claimNext(ctx, p.stream, p.sub, p.ackWait); m != nil {
			mctx, msg := p.bus.wrapDelivery(ctx, p.stream, p.sub, m, p.ackWait)
			return mctx, msg, nil
		}
		select {
		case <-ctx.Done():
			return ctx, nil, ctx.Err()
		case <-p.bus.done:
			return ctx, nil, events.ErrClosed
		case <-p.bus.clock.After(pollInterval):
		}
	}
}

// Stop implements events.Puller. membus keeps no state beyond the durable's
// shared claim state, which every other puller and Subscribe on it still
// need, so there is nothing here to release.
func (p *puller) Stop() {}
