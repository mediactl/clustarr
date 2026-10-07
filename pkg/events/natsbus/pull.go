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

package natsbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.PullSubscriber = (*Bus)(nil)

// puller is one Pull call's handle on a durable pull consumer: the consumer
// itself, and the MAX_DELIVERIES watcher that keeps a hung caller's lease
// from being lost silently, exactly as Subscribe's does.
type puller struct {
	bus       *Bus
	cons      jetstream.Consumer
	sub       events.Subscription
	stopWatch func()
	stopOnce  sync.Once
}

// Pull implements events.PullSubscriber. It is the one natsbus path that
// writes consumer config: a transcode pool's durable is dynamic, created per
// profile and class here rather than by the topology, and deleted with its
// watcher by squasharr's withdrawal path (StreamAdmin.DeleteSubscription), so
// Pull creates both, the durable with the topology's cap
// (s.MaxAckPending, events.SubscriptionSpec), not its one slot.
func (b *Bus) Pull(ctx context.Context, s events.Subscription) (events.Puller, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	spec := events.SubscriptionSpec(s)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, s.Stream, events.ConsumerConfig(spec))
	if err != nil {
		return nil, fmt.Errorf("natsbus: pull %s/%s: %w", s.Stream, s.Durable, err)
	}
	watcher := events.DeadLetterWatcherSpec(spec)
	if _, err := b.js.CreateOrUpdateConsumer(ctx, events.StreamAdvisories, events.ConsumerConfig(watcher)); err != nil {
		return nil, fmt.Errorf("natsbus: pull %s/%s: watcher: %w", s.Stream, s.Durable, err)
	}
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	unhook := context.AfterFunc(b.serveCtx, cancel)
	done := make(chan struct{})
	go func() { defer close(done); b.runWatcher(wctx, s) }()
	stopWatch := func() { unhook(); cancel(); <-done }
	return &puller{bus: b, cons: cons, sub: s, stopWatch: stopWatch}, nil
}

// Next implements events.Puller. It fetches exactly one message per call and
// wakes every second to notice a cancelled ctx.
func (p *puller) Next(ctx context.Context) (context.Context, events.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ctx, nil, err
		}
		jm, err := p.cons.Next(jetstream.FetchMaxWait(time.Second))
		if errors.Is(err, jetstream.ErrNoMessages) || errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			return ctx, nil, fmt.Errorf("natsbus: next %s/%s: %w", p.sub.Stream, p.sub.Durable, err)
		}
		mctx, m, err := p.bus.receive(ctx, jm, p.sub, deliveryHooks{})
		if err != nil {
			return ctx, nil, err
		}
		return mctx, m, nil
	}
}

// Stop implements events.Puller. It stops the watcher and waits for it.
func (p *puller) Stop() {
	p.stopOnce.Do(func() {
		if p.stopWatch != nil {
			p.stopWatch()
		}
	})
}
