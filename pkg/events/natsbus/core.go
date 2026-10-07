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
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.CoreSubscriber = (*Bus)(nil)

// coreSub is one SubscribeCore call's subscription, which the bus tracks so
// Drain drains it and Close ends it.
type coreSub struct {
	bus  *Bus
	sub  *nats.Subscription
	once sync.Once
}

// stop unsubscribes once and forgets the subscription.
func (c *coreSub) stop() {
	c.once.Do(func() {
		_ = unsubscribe(c.sub)
		c.bus.forgetCore(c)
	})
}

// forgetCore drops a stopped core subscription from the bus's list.
func (b *Bus) forgetCore(c *coreSub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, x := range b.cores {
		if x == c {
			b.cores = append(b.cores[:i], b.cores[i+1:]...)
			return
		}
	}
}

// SubscribeCore implements events.CoreSubscriber: a plain core NATS
// subscription (nc.Subscribe), no stream and no durable, ended by the stop
// function, by ctx, or by the bus's Drain or Close. h runs on the
// subscription's delivery goroutine.
func (b *Bus) SubscribeCore(ctx context.Context, subject string, h func(subject string, data []byte)) (func(), error) {
	if !b.accepting() {
		return nil, events.ErrClosed
	}
	sub, err := b.nc.Subscribe(subject, func(m *nats.Msg) { h(m.Subject, m.Data) })
	if err != nil {
		return nil, fmt.Errorf("natsbus: subscribe %s: %w", subject, err)
	}
	c := &coreSub{bus: b, sub: sub}
	b.mu.Lock()
	if b.closed || b.draining {
		b.mu.Unlock()
		_ = unsubscribe(sub)
		return nil, events.ErrClosed
	}
	b.cores = append(b.cores, c)
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.stop()
	}()
	return c.stop, nil
}
