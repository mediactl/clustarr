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

	"github.com/mediactl/clustarr/pkg/events"
)

// responderDrainGrace is how long past the bus's request timeout a
// responder's stop waits for its running handlers before it cancels them:
// a handler's deadline is at most the request timeout, measured from
// receipt, so one that is still running past it ignores its context.
const responderDrainGrace = 5 * time.Second

// responder is one Serve call's running state: its queue subscription, the
// context its handlers run under, and the respond goroutines it started
// (ADR-0020 §1).
type responder struct {
	bus     *Bus
	subject string
	queue   string
	cb      nats.MsgHandler
	limits  events.ServeLimits

	// ctx parents every handler of this responder; cancel ends them, which
	// stop does after its drain and Close does at once. It is a child of
	// the bus's serve context, so Close's serveCancel ends it too.
	ctx    context.Context
	cancel context.CancelFunc

	// running counts the respond goroutines started and not yet returned.
	// Its Add happens only in the subscription's callback, so a Wait
	// started after the subscription has closed cannot race one.
	running sync.WaitGroup

	mu       sync.Mutex
	sub      *nats.Subscription // nil while away from the queue group, or stopped
	stopping bool

	stopOnce sync.Once
	stopErr  error
}

// subscribe joins the queue group (or subscribes plainly with no queue),
// its client-side buffer bounded by the Serve's limits (ADR-0020 §2).
func (r *responder) subscribe() (*nats.Subscription, error) {
	var (
		sub *nats.Subscription
		err error
	)
	if r.queue == "" {
		sub, err = r.bus.nc.Subscribe(r.subject, r.cb)
	} else {
		sub, err = r.bus.nc.QueueSubscribe(r.subject, r.queue, r.cb)
	}
	if err != nil {
		return nil, err
	}
	if err := sub.SetPendingLimits(r.limits.MaxPending, r.limits.MaxPendingBytes); err != nil {
		_ = unsubscribe(sub)
		return nil, fmt.Errorf("pending limits: %w", err)
	}
	return sub, nil
}

// drain takes r out of service gracefully: the subscription is drained
// (nats.go removes interest at the server and still delivers what it has
// buffered), then r waits for the respond goroutines until ctx ends, and only
// then cancels its handlers' context. It is idempotent; a second call returns
// the first's result.
func (r *responder) drain(ctx context.Context) error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopping = true
		sub := r.sub
		r.sub = nil
		r.mu.Unlock()

		if sub != nil {
			if err := drainSubscription(ctx, sub); err != nil {
				r.cancel()
				r.stopErr = err
				return
			}
		}
		idle := make(chan struct{})
		go func() { r.running.Wait(); close(idle) }()
		select {
		case <-idle:
		case <-ctx.Done():
			r.stopErr = ctx.Err()
		}
		r.cancel()
	})
	return r.stopErr
}

// halt is Close's stop: the handlers' context ends at once and the
// subscription is removed without a drain.
func (r *responder) halt() error {
	r.mu.Lock()
	r.stopping = true
	sub := r.sub
	r.sub = nil
	r.mu.Unlock()
	r.cancel()
	if sub == nil {
		return nil
	}
	return unsubscribe(sub)
}

// drainSubscription drains a core subscription and waits until nats.go has
// delivered everything it buffered and closed it, or ctx ends, when it
// unsubscribes it outright. A subscription the connection already dropped is
// drained.
func drainSubscription(ctx context.Context, sub *nats.Subscription) error {
	closed := sub.StatusChanged(nats.SubscriptionClosed)
	if err := sub.Drain(); err != nil {
		if errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrBadSubscription) ||
			errors.Is(err, nats.ErrConnectionDraining) {
			return nil
		}
		return errors.Join(err, unsubscribe(sub))
	}
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		_ = unsubscribe(sub)
		return ctx.Err()
	}
}

// stopBudget bounds a responder's own stop: the longest a handler may run
// (the bus's request timeout, which caps every handler's deadline) plus a
// grace. A bus with no request timeout uses DefaultRequestTimeout's.
func (b *Bus) stopBudget() time.Duration {
	d := b.opts.requestTimeout
	if d <= 0 {
		d = DefaultRequestTimeout
	}
	return d + responderDrainGrace
}

// forgetResponder drops a stopped responder from the bus's list.
func (b *Bus) forgetResponder(r *responder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, x := range b.responders {
		if x == r {
			b.responders = append(b.responders[:i], b.responders[i+1:]...)
			return
		}
	}
}

// Drain implements events.Bus (ADR-0020 §1): every pull subscription stops
// as its stop function would, every responder and core subscription drains,
// and the bus waits for their handlers until ctx ends. Then it cancels the
// handlers' context and closes the bus as Close does. It does not touch the
// NATS connection: pkg/busconn.Shutdown drains that afterwards, so the acks,
// replies and KV writes the draining handlers made still reach the server.
func (b *Bus) Drain(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.draining = true
	subs := append([]*subscription(nil), b.subs...)
	responders := append([]*responder(nil), b.responders...)
	cores := append([]*coreSub(nil), b.cores...)
	b.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Go(s.stop)
	}
	for _, r := range responders {
		wg.Go(func() { _ = r.drain(ctx) })
	}
	for _, c := range cores {
		wg.Go(func() { _ = drainSubscription(ctx, c.sub) })
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}

	// What is still running past ctx is halted, as Close halts it.
	b.mu.Lock()
	b.closed = true
	b.responders, b.subs, b.cores = nil, nil, nil
	b.mu.Unlock()
	b.serveCancel()
	for _, s := range subs {
		s.halt()
	}
	for _, r := range responders {
		_ = r.halt()
	}
	for _, c := range cores {
		c.stop()
	}
	return err
}
