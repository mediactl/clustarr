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
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The pull loop's timing.
const (
	fetchWait      = 30 * time.Second
	fetchHeartbeat = 5 * time.Second
	fetchRetry     = time.Second
	reapEvery      = 250 * time.Millisecond
	// lapseGrace is how far past the broker's deadline a delivery runs
	// before its slot is reclaimed: the broker timed it from its own
	// delivery, a little before dispatch.
	lapseGrace = time.Second
)

// delivery is one running handler's message.
type delivery struct {
	seq, attempt uint64
	deadline     time.Time // the broker's redelivery deadline; InProgress moves it
	lapsed       bool
}

// parkedMsg is a delivery waiting for a slot.
type parkedMsg struct {
	msg jetstream.Msg
	// seq is the message's stream sequence, 0 when its metadata is
	// unreadable, which parks it without replacing anything.
	seq, attempt uint64
}

// A subscription is one Subscribe call's running state: a slot-gated pull,
// the handlers it has started, and what it has parked (spec §9.3).
//
// Slots. The subscription runs at most slots handlers (Subscription.MaxInFlight)
// at once in this process, and it asks the broker only for as many messages
// as it has free slots: Fetch(slots-live). The durable's MaxAckPending is a
// different number, the cap across every process that consumes it, which the
// topology sets. JetStream's Consume, which this replaced, pulled again as
// soon as its callback returned, so one process took as much of that cap as
// the broker would hand it and parked what it could not run, starving the
// other replicas.
//
// Lapses. A handler past the broker's acknowledgement deadline for its
// delivery (events.AckDeadline: Backoff[n-1] for delivery n, else AckWait;
// an InProgress moves it) plus lapseGrace has lapsed: the broker will
// deliver the message again, to this process or another. A lapsed delivery
// gives its slot back, so one hung handler cannot stop the next task, but
// only while fewer than slots are lapsed (the lapsed cap). Without the cap a
// handler that is merely slow would fetch a replacement at every lapse, and
// handlers running in one pod would be bounded only by the durable's
// MaxAckPending. With it, they never exceed twice the slots: slots live and
// at most slots lapsed. A lapsed handler keeps running with its context; its
// late settlement behaves as any late settlement does.
//
// A free slot always means an outstanding Fetch. JetStream raises the
// MAX_DELIVERIES advisory, from which the dead-letter watcher copies a
// message whose final delivery lapsed, only when it next tries to deliver,
// which it does only to a waiting pull request (nats-server getNextMsg;
// CLAUDE.md's gotcha). So the pull loop keeps a Fetch open whenever a slot
// is free, and once the lapsed cap is reached with every slot held it keeps
// one Fetch(1) open regardless, parking what arrives. Parked deliveries run
// nothing: each one starts when a slot frees, or lapses at the broker and is
// delivered again, and a later delivery of a message already parked replaces
// it. What one process holds stays bounded by the durable's MaxAckPending.
//
// Drain. Once the subscription stops -- its context ends or its stop
// function is called -- it fetches nothing more, drops what it parked (the
// broker delivers it again after its deadline), and lets running handlers
// keep their context for up to Subscription.Drain before cancelling it.
// Close halts at once, without a drain.
//
// Re-binding. The pull loop binds a durable it never creates
// (Bus.bindConsumer). If the durable or its stream disappears under it -- a
// NATS restart wipes a memory-backed stream until the manager ensures the
// topology again -- it stops fetching, keeps its running handlers, and binds
// again once the durable exists (spec §5.9).
type subscription struct {
	bus   *Bus
	sub   events.Subscription
	slots int
	run   func(ctx context.Context, m jetstream.Msg, onProgress func())
	watch jetstream.ConsumeContext // the dead-letter watcher

	loopCtx        context.Context
	stopLoop       context.CancelFunc
	handlerCtx     context.Context
	cancelHandlers context.CancelFunc
	hard           chan struct{} // closed by halt: no drain
	hardOnce       sync.Once
	drained        chan struct{}

	mu       sync.Mutex
	stopping bool
	running  map[*delivery]struct{}
	live     int // running deliveries not lapsed: they hold the slots
	lapsed   int // running deliveries past their deadline: at most slots
	parked   []parkedMsg
	wake     chan struct{} // capacity 1
	handlers sync.WaitGroup
	loops    sync.WaitGroup
}

func newSubscription(ctx context.Context, b *Bus, sub events.Subscription,
	run func(context.Context, jetstream.Msg, func()),
) *subscription {
	s := &subscription{
		bus: b, sub: sub, slots: max(sub.MaxInFlight, 1), run: run,
		hard: make(chan struct{}), drained: make(chan struct{}),
		running: map[*delivery]struct{}{}, wake: make(chan struct{}, 1),
	}
	s.loopCtx, s.stopLoop = context.WithCancel(ctx)
	// Handlers outlive the loop by up to sub.Drain, so their context is not
	// the subscription's, though it keeps its values.
	s.handlerCtx, s.cancelHandlers = context.WithCancel(context.WithoutCancel(ctx))
	return s
}

func (s *subscription) start() {
	s.loops.Add(2)
	go s.pullLoop()
	go s.reapLoop()
	go s.drainOnStop()
}

// next starts what parked deliveries it can and says how many messages to
// fetch: the free slots; one at the lapsed cap even with every slot held, so
// a pull always waits for JetStream to raise MAX_DELIVERIES on; else none.
func (s *subscription) next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startParkedLocked()
	switch {
	case s.live < s.slots:
		return s.slots - s.live
	case s.lapsed >= s.slots:
		return 1
	default:
		return 0
	}
}

func (s *subscription) pullLoop() {
	defer s.loops.Done()
	ctx := s.loopCtx
	var cons jetstream.Consumer
	for ctx.Err() == nil {
		if cons == nil {
			c, err := s.bus.bindConsumer(ctx, s.sub.Stream, s.sub.Durable)
			if err != nil {
				return
			}
			cons = c
		}
		n := s.next()
		if n == 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		err := s.fetch(ctx, cons, n)
		switch {
		case err == nil, ctx.Err() != nil, benignFetchError(err):
		case consumerGone(err):
			logging.FromContext(ctx).Warn("bus: durable gone; waiting for the manager to re-create it",
				"stream", s.sub.Stream, "durable", s.sub.Durable, "error", err)
			cons = nil
		default:
			logging.FromContext(ctx).Warn("bus: fetch failed; retrying",
				"durable", s.sub.Durable, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(fetchRetry):
			}
		}
	}
}

// fetch asks for n messages and dispatches each as it arrives. Its request
// waits up to fetchWait at the broker; one this process abandons stays there
// until then, which is how a stopped pod's last pull still lets JetStream
// raise MAX_DELIVERIES.
func (s *subscription) fetch(ctx context.Context, cons jetstream.Consumer, n int) error {
	fctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	batch, err := cons.Fetch(n, jetstream.FetchContext(fctx), jetstream.FetchHeartbeat(fetchHeartbeat))
	if err != nil {
		return err
	}
	for m := range batch.Messages() {
		s.dispatch(m)
	}
	return batch.Error()
}

// dispatch starts a handler for m when a slot is free and parks it otherwise.
// A fresh delivery starts even while an earlier delivery of the same message
// runs: JetStream has given up on that one.
func (s *subscription) dispatch(m jetstream.Msg) {
	var seq, attempt uint64 = 0, 1
	if md, err := m.Metadata(); err == nil {
		seq = md.Sequence.Stream
		attempt = max(md.NumDelivered, 1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return // left unsettled: JetStream makes it again after its deadline
	}
	if s.live < s.slots {
		s.startLocked(m, seq, attempt)
		return
	}
	p := parkedMsg{msg: m, seq: seq, attempt: attempt}
	if seq != 0 {
		for i := range s.parked {
			if s.parked[i].seq == seq {
				s.parked[i] = p // a later delivery of a message already waiting
				return
			}
		}
	}
	s.parked = append(s.parked, p)
}

// startParkedLocked starts parked deliveries, oldest first, while a slot is
// free, skipping any whose message still has a handler running.
func (s *subscription) startParkedLocked() {
	for i := 0; i < len(s.parked) && s.live < s.slots && !s.stopping; {
		p := s.parked[i]
		if p.seq != 0 && s.seqRunningLocked(p.seq) {
			i++
			continue
		}
		s.parked = append(s.parked[:i], s.parked[i+1:]...)
		s.startLocked(p.msg, p.seq, p.attempt)
	}
}

func (s *subscription) seqRunningLocked(seq uint64) bool {
	for d := range s.running {
		if d.seq == seq {
			return true
		}
	}
	return false
}

func (s *subscription) startLocked(m jetstream.Msg, seq, attempt uint64) {
	d := &delivery{seq: seq, attempt: attempt, deadline: time.Now().Add(events.AckDeadline(s.sub, attempt))}
	s.running[d] = struct{}{}
	s.live++
	s.handlers.Add(1)
	go func() {
		defer s.handlers.Done()
		s.run(s.handlerCtx, m, func() { s.progress(d) })
		s.finish(d)
	}()
}

// finish returns d's slot, or its place under the lapsed cap, and hands a
// free slot to the oldest startable parked delivery.
func (s *subscription) finish(d *delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, d)
	if d.lapsed {
		s.lapsed--
	} else {
		s.live--
	}
	s.startParkedLocked()
	s.signalLocked()
}

// progress moves d's deadline on an InProgress. A lapsed delivery stays
// lapsed: JetStream may already have made it again.
func (s *subscription) progress(d *delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !d.lapsed {
		d.deadline = time.Now().Add(events.AckDeadline(s.sub, d.attempt))
	}
}

// reap marks running deliveries past their deadline lapsed, freeing their
// slots, while fewer than slots are lapsed (spec §9.3, "The bound").
func (s *subscription) reap(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for d := range s.running {
		if d.lapsed || s.lapsed >= s.slots || !now.After(d.deadline.Add(lapseGrace)) {
			continue
		}
		d.lapsed = true
		s.live--
		s.lapsed++
		changed = true
	}
	if changed {
		s.startParkedLocked()
		s.signalLocked()
	}
}

func (s *subscription) reapLoop() {
	defer s.loops.Done()
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-s.loopCtx.Done():
			return
		case now := <-t.C:
			s.reap(now)
		}
	}
}

func (s *subscription) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// drainOnStop waits for the subscription to stop, lets running handlers keep
// their context for up to sub.Drain unless halted, then cancels it.
func (s *subscription) drainOnStop() {
	select {
	case <-s.loopCtx.Done():
	case <-s.hard:
	}
	s.mu.Lock()
	s.stopping = true
	s.parked = nil
	s.mu.Unlock()
	if d := s.sub.Drain; d > 0 {
		idle := make(chan struct{})
		go func() { s.handlers.Wait(); close(idle) }()
		t := time.NewTimer(d)
		select {
		case <-idle:
		case <-t.C:
		case <-s.hard:
		}
		t.Stop()
	}
	s.cancelHandlers()
	close(s.drained)
}

// stop ends the subscription: no more fetches, running handlers keep their
// context for up to sub.Drain, and it returns once every handler and loop has.
func (s *subscription) stop() {
	s.stopLoop()
	if s.watch != nil {
		s.watch.Stop()
	}
	<-s.drained
	s.handlers.Wait()
	s.loops.Wait()
}

// halt is Close's stop: handlers' context ends at once, and nothing waits.
func (s *subscription) halt() {
	s.hardOnce.Do(func() { close(s.hard) })
	s.stopLoop()
	if s.watch != nil {
		s.watch.Stop()
	}
}
