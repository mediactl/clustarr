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
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// subscription is one Subscribe call's running state: its pull, its
// MAX_DELIVERIES watcher, and the handlers it has started or parked.
//
// JetStream's Consume calls back one message at a time, and a callback that
// blocks issues no further pull requests. So the callback never blocks: it
// starts a handler when one of the subscription's slots is free and parks the
// delivery otherwise, and a handler that finishes starts the oldest parked
// one. Two things depend on the pull staying open while every slot is taken.
// A handler hung on an early delivery must not stall everything else, which
// it would by holding the one callback. And JetStream announces a message
// whose final delivery lapsed (the MAX_DELIVERIES advisory the watcher
// dead-letters from) only when it tries to deliver it once more, which it
// does only to a waiting pull: a subscription whose slots are all held by
// hung handlers must keep asking, or the advisory never comes.
//
// Parking cannot grow without bound. JetStream delivers nothing beyond
// MaxAckPending (MaxInFlight) unacknowledged messages, across every replica,
// and a parked delivery is replaced by a later delivery of the same message
// rather than queued behind it, so at most MaxInFlight are ever parked. In
// practice what parks is another delivery of a message whose handler is hung
// or slow past its acknowledgement deadline.
type subscription struct {
	// slots is how many handlers may run at once.
	slots int

	// cancel cancels the context every handler is given.
	cancel context.CancelFunc

	watch *nats.Subscription
	run   func(jetstream.Msg)

	mu       sync.Mutex
	halted   bool
	running  int
	parked   []parkedMsg
	consume  jetstream.ConsumeContext
	handlers sync.WaitGroup
}

// parkedMsg is a delivery waiting for a handler slot.
type parkedMsg struct {
	msg jetstream.Msg
	// seq is the message's stream sequence, 0 when its metadata is
	// unreadable, which parks it without replacing anything.
	seq uint64
}

func newSubscription(maxInFlight int, cancel context.CancelFunc, watch *nats.Subscription,
	run func(jetstream.Msg),
) *subscription {
	return &subscription{
		slots:  max(maxInFlight, 1),
		cancel: cancel,
		watch:  watch,
		run:    run,
	}
}

// setConsume records the pull once Consume has started it. A halt that raced
// ahead of it stops it here instead.
func (s *subscription) setConsume(c jetstream.ConsumeContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.halted {
		c.Stop()
		return
	}
	s.consume = c
}

// dispatch is the Consume callback. It starts a handler for m if a slot is
// free and parks m otherwise; it never blocks.
func (s *subscription) dispatch(m jetstream.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.halted {
		return
	}
	if s.running < s.slots {
		s.startLocked(m)
		return
	}
	p := parkedMsg{msg: m}
	if md, err := m.Metadata(); err == nil {
		p.seq = md.Sequence.Stream
	}
	if p.seq != 0 {
		for i := range s.parked {
			if s.parked[i].seq == p.seq {
				// A later delivery of a message already waiting: JetStream
				// has given up on the earlier one, so only the later one is
				// worth a handler.
				s.parked[i] = p
				return
			}
		}
	}
	s.parked = append(s.parked, p)
}

// startLocked runs a handler for m on its own goroutine, and on return hands
// its slot to the oldest parked delivery. The caller holds s.mu.
func (s *subscription) startLocked(m jetstream.Msg) {
	s.running++
	s.handlers.Add(1)
	go func() {
		defer s.handlers.Done()
		s.run(m)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.running--
		if s.halted || len(s.parked) == 0 {
			return
		}
		next := s.parked[0].msg
		s.parked[0] = parkedMsg{}
		s.parked = s.parked[1:]
		s.startLocked(next)
	}()
}

// halt cancels the handlers' context and stops the pull and the watcher. It
// does not wait for running handlers; see wait. Parked deliveries are left
// unsettled: JetStream makes them again once their acknowledgement deadlines
// pass, to whichever replica is consuming. It is idempotent.
func (s *subscription) halt() error {
	s.mu.Lock()
	if s.halted {
		s.mu.Unlock()
		return nil
	}
	s.halted = true
	s.parked = nil
	c := s.consume
	s.mu.Unlock()

	s.cancel()
	if c != nil {
		c.Stop()
	}
	return unsubscribe(s.watch)
}

// wait blocks until every handler the subscription started has returned.
// Call it after halt, which guarantees none starts afterwards.
func (s *subscription) wait() { s.handlers.Wait() }
