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
	"cmp"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
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
	lapsedAt     time.Time // when the reaper marked it lapsed

	// cancel ends a budgeted delivery's handler context with a cause; nil
	// without a budget (Subscription.HandlerTimeout). cancelled records that
	// the reaper has cancelled it with events.ErrLapsed.
	cancel    context.CancelCauseFunc
	cancelled bool
}

// A subscription is one Subscribe call's running state: a slot-gated pull
// and the handlers it has started (spec §9.3).
//
// Slots. The subscription runs at most slots handlers (Subscription.MaxInFlight)
// at once in this process, and it asks the broker only for as many messages
// as it has free slots: Fetch(slots-live). The durable's MaxAckPending is a
// different number, the cap across every process that consumes it, which the
// topology sets. JetStream's Consume, which this replaced, pulled again as
// soon as its callback returned, so one process took as much of that cap as
// the broker would hand it and held what it could not run, starving the
// other replicas. At saturation it fetches nothing.
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
// Budget. With Subscription.HandlerTimeout set, the bus heartbeats each
// running handler every third of its deadline, so a budgeted handler lapses
// only when its process stalls or its budget is spent; runs it under a context
// whose deadline is the budget (cause events.ErrHandlerBudget); and cancels a
// lapsed one's context with cause events.ErrLapsed one deadline after the
// lapse. Without one nothing changes. For every subscription, a lapsed cap
// held longer than the handler budget plus the first-delivery deadline is a
// wedge (wedged, Bus.Wedged): handlers that ignore their context, which only
// a restart frees (split §3.3 and §9.3 as amended 2026-10-07, S1).
//
// MAX_DELIVERIES. JetStream raises the advisory from which the dead-letter
// watcher copies a message whose final delivery lapsed only when it next
// tries to deliver to a waiting pull, for MaxDeliver 2 or more: nats-server's
// deliveryCount returns redeliveries, so the ack timer (checkPending) catches
// only MaxDeliver 1, and getNextMsg raises the rest. This loop keeps no pull
// open for it. The next pull from any replica, or from this one once a slot
// frees, raises it; the lag metric's ack-pending wakes a domain at zero; and a
// final-attempt handler that returns is dead-lettered in process by Settle.
// A pull this process abandons does not stand in for one: nats.go
// unsubscribes its inbox, and any CONSUMER.INFO -- the manager's QueueGauge,
// every 30 s -- prunes it (worker-pool research, 2026-10-07, D4 and E5).
//
// Drain. Once the subscription stops -- its context ends or its stop
// function is called -- it fetches nothing more, hands back with a Nak any
// delivery still arriving, and lets running handlers keep their context for
// up to Subscription.Drain before cancelling it. Close halts at once, without
// a drain.
//
// Re-binding. The pull loop binds a durable it never creates
// (Bus.bindConsumer), and the dead-letter watcher loop binds the durable's
// watcher the same way. If the durable or its stream disappears under it -- a
// NATS restart wipes a memory-backed stream until the manager ensures the
// topology again -- it stops fetching, keeps its running handlers, and binds
// again once the durable exists (spec §5.9).
type subscription struct {
	bus   *Bus
	sub   events.Subscription
	slots int
	run   func(ctx context.Context, eff events.Subscription, m jetstream.Msg, h deliveryHooks)

	loopCtx        context.Context
	stopLoop       context.CancelFunc
	handlerCtx     context.Context
	cancelHandlers context.CancelFunc
	hard           chan struct{} // closed by halt: no drain
	hardOnce       sync.Once
	drained        chan struct{}

	mu       sync.Mutex
	stopping bool
	// timing is the bound durable's, as the broker stores it: deadlines,
	// delayed naks and Settle read it, not the caller's sub (S5). It is the
	// caller's until the first bind.
	timing  events.Timing
	running map[*delivery]struct{}
	live    int // running deliveries not lapsed: they hold the slots
	lapsed  int // running deliveries past their deadline: at most slots
	// saturatedSince is when lapsed deliveries came to hold the lapsed cap,
	// zero while they do not.
	saturatedSince time.Time
	wake           chan struct{} // capacity 1
	handlers       sync.WaitGroup
	loops          sync.WaitGroup
}

// deliveryHooks is what a subscription tells the message it hands a handler:
// onProgress moves the delivery's lapse deadline on every InProgress, and
// lapsed reports that the delivery has given its slot back, after which its
// InProgress and Nak are muted (message.lapsed). Pull passes the zero value.
type deliveryHooks struct {
	onProgress func()
	lapsed     func() bool
}

func newSubscription(ctx context.Context, b *Bus, sub events.Subscription,
	run func(context.Context, events.Subscription, jetstream.Msg, deliveryHooks),
) *subscription {
	s := &subscription{
		bus: b, sub: sub, slots: max(sub.MaxInFlight, 1), run: run, timing: sub.Timing(),
		hard: make(chan struct{}), drained: make(chan struct{}),
		running: map[*delivery]struct{}{}, wake: make(chan struct{}, 1),
	}
	s.loopCtx, s.stopLoop = context.WithCancel(ctx)
	// Handlers outlive the loop by up to sub.Drain, so their context is not
	// the subscription's, though it keeps its values.
	s.handlerCtx, s.cancelHandlers = context.WithCancel(context.WithoutCancel(ctx))
	return s
}

// start runs the pull loop, the lapse reaper and the durable's dead-letter
// watcher (Bus.runWatcher), all until the subscription stops, and the drain
// that follows.
func (s *subscription) start() {
	s.loops.Add(3)
	go s.pullLoop()
	go s.reapLoop()
	go func() {
		defer s.loops.Done()
		s.bus.runWatcher(s.loopCtx, s.sub)
	}()
	go s.drainOnStop()
}

// next says how many messages to fetch: the free slots, and none at
// saturation. A message this process cannot run stays Pending at the broker,
// spends no delivery attempt and stays available to other replicas (split
// §9.3 as amended 2026-10-07, M1). Keeping a Fetch(1) open at the lapsed cap,
// as Wave 4c did, drew message after message into a park where each lapsed and
// was redelivered, an attempt spent per redelivery, until it was dead-lettered
// without ever running.
func (s *subscription) next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(s.slots-s.live, 0)
}

func (s *subscription) pullLoop() {
	defer s.loops.Done()
	ctx := s.loopCtx
	var cons jetstream.Consumer
	for ctx.Err() == nil {
		if cons == nil {
			c, bound, err := s.bus.bindConsumer(ctx, s.sub.Stream, s.sub.Durable)
			if err != nil {
				return
			}
			cons = c
			s.bindTiming(ctx, bound)
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

// bindTiming stores the bound durable's timing and warns, once per bind, when
// it differs from what this process was compiled with (split §9.2 as amended
// 2026-10-07, S5; research D5).
func (s *subscription) bindTiming(ctx context.Context, bound events.Timing) {
	s.mu.Lock()
	s.timing = bound
	s.mu.Unlock()
	if declared := s.sub.Timing(); !declared.Equal(bound) {
		logging.FromContext(ctx).Warn("bus: the durable's timing differs from this process's; using the durable's (version skew?)",
			"stream", s.sub.Stream, "durable", s.sub.Durable,
			"declared_ack_wait", declared.AckWait, "declared_backoff", declared.Backoff,
			"declared_max_deliver", declared.MaxDeliver,
			"bound_ack_wait", bound.AckWait, "bound_backoff", bound.Backoff,
			"bound_max_deliver", bound.MaxDeliver)
	}
}

// effectiveLocked is the caller's subscription with the bound durable's
// timing. The caller holds s.mu.
func (s *subscription) effectiveLocked() events.Subscription {
	return s.sub.WithTiming(s.timing)
}

// fetch asks for n messages and dispatches each as it arrives. Its request
// waits up to fetchWait at the broker; one this process abandons is pruned by
// the next CONSUMER.INFO (nats-server consumer.go; worker-pool research E5),
// so nothing relies on it.
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

// dispatch starts a handler for m. A fetch asks only for free slots and slots
// only free up while it waits, so a slot is always free here; if one is not, or
// the subscription is stopping, m goes straight back with a plain Nak, so
// another replica gets it now rather than after its deadline (research C8). A
// fresh delivery starts even while an earlier delivery of the same message
// runs: JetStream has given up on that one.
func (s *subscription) dispatch(m jetstream.Msg) {
	var seq, attempt uint64 = 0, 1
	if md, err := m.Metadata(); err == nil {
		seq = md.Sequence.Stream
		attempt = max(md.NumDelivered, 1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.live >= s.slots {
		if !s.stopping {
			logging.FromContext(s.loopCtx).Warn("bus: a fetch returned more than the free slots; handing the message back",
				"durable", s.sub.Durable, "stream_seq", seq)
		}
		_ = m.Nak()
		return
	}
	s.startLocked(m, seq, attempt)
}

func (s *subscription) startLocked(m jetstream.Msg, seq, attempt uint64) {
	eff := s.effectiveLocked()
	d := &delivery{seq: seq, attempt: attempt, deadline: time.Now().Add(events.AckDeadline(eff, attempt))}
	s.running[d] = struct{}{}
	s.live++
	hctx, done := s.handlerCtx, func() {}
	if budget := s.sub.HandlerTimeout; budget > 0 {
		cctx, cancel := context.WithCancelCause(s.handlerCtx)
		bctx, stopBudget := context.WithDeadlineCause(cctx, time.Now().Add(budget), events.ErrHandlerBudget)
		d.cancel = cancel
		beat := make(chan struct{})
		go s.heartbeat(bctx, m, d, beat) // until beat closes or bctx ends
		hctx = bctx
		done = func() { close(beat); stopBudget(); cancel(nil) }
	}
	s.handlers.Add(1)
	go func() {
		defer s.handlers.Done()
		s.run(hctx, eff, m, deliveryHooks{
			onProgress: func() { s.progress(d) },
			lapsed:     func() bool { s.mu.Lock(); defer s.mu.Unlock(); return d.lapsed },
		})
		done()
		s.finish(d)
	}()
}

// heartbeat is the bus's keep-alive for a budgeted delivery: InProgress every
// third of its deadline while the handler runs, its budget is unspent and it
// has not lapsed (split §9.3 as amended, S1).
func (s *subscription) heartbeat(ctx context.Context, m jetstream.Msg, d *delivery, done <-chan struct{}) {
	s.mu.Lock()
	every := events.AckDeadline(s.effectiveLocked(), d.attempt) / 3
	s.mu.Unlock()
	t := time.NewTicker(max(every, time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return // the budget is spent, or the delivery was cancelled
		case <-t.C:
			s.mu.Lock()
			lapsed := d.lapsed
			s.mu.Unlock()
			if lapsed {
				return
			}
			_ = m.InProgress()
			s.progress(d)
		}
	}
}

// finish returns d's slot, or its place under the lapsed cap, and wakes the
// loop to fetch for the freed slot.
func (s *subscription) finish(d *delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, d)
	if d.lapsed {
		s.lapsed--
	} else {
		s.live--
	}
	s.saturationLocked(time.Now())
	s.signalLocked()
}

// saturationLocked keeps saturatedSince current and the lapse gauges set.
// The caller holds s.mu.
func (s *subscription) saturationLocked(now time.Time) {
	if s.lapsed >= s.slots {
		if s.saturatedSince.IsZero() {
			s.saturatedSince = now
		}
	} else {
		s.saturatedSince = time.Time{}
	}
	metrics.BusLapsedHandlers.WithLabelValues(s.sub.Durable).Set(float64(s.lapsed))
	metrics.BusSaturated.WithLabelValues(s.sub.Durable).Set(boolGauge(s.lapsed >= s.slots))
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// wedged reports a subscription whose every slot lapsed handlers have held for
// longer than its budget plus a first-delivery deadline: handlers that ignore
// their context, which only a restart frees (split §3.3, §9.3 as amended).
func (s *subscription) wedged(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.saturatedSince.IsZero() {
		return nil
	}
	budget := cmp.Or(s.sub.HandlerTimeout, s.sub.AckWait, events.DefaultAckWait)
	limit := budget + events.AckDeadline(s.effectiveLocked(), 1)
	if held := now.Sub(s.saturatedSince); held > limit {
		return fmt.Errorf("bus: every slot of %s has been held by lapsed handlers for %v (limit %v): they ignore their context",
			s.sub.Durable, held.Round(time.Second), limit)
	}
	return nil
}

// progress moves d's deadline on an InProgress. A lapsed delivery stays
// lapsed: JetStream may already have made it again.
func (s *subscription) progress(d *delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !d.lapsed {
		d.deadline = time.Now().Add(events.AckDeadline(s.effectiveLocked(), d.attempt))
	}
}

// reap marks running deliveries past their deadline lapsed, freeing their
// slots, while fewer than slots are lapsed (spec §9.3, "The bound"), and
// cancels a budgeted lapsed delivery's handler one deadline after its lapse
// (S1).
func (s *subscription) reap(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	eff := s.effectiveLocked()
	for d := range s.running {
		if d.lapsed {
			if d.cancel != nil && !d.cancelled && now.After(d.lapsedAt.Add(events.AckDeadline(eff, d.attempt))) {
				d.cancel(events.ErrLapsed)
				d.cancelled = true
			}
			continue
		}
		if s.lapsed >= s.slots || !now.After(d.deadline.Add(lapseGrace)) {
			continue
		}
		d.lapsed = true
		d.lapsedAt = now
		s.live--
		s.lapsed++
		changed = true
	}
	s.saturationLocked(now)
	if changed {
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
// The bus then forgets it, so Bus.Wedged no longer reads it.
func (s *subscription) stop() {
	s.stopLoop()
	<-s.drained
	s.handlers.Wait()
	s.loops.Wait()
	s.bus.forget(s)
	metrics.BusLapsedHandlers.DeleteLabelValues(s.sub.Durable)
	metrics.BusSaturated.DeleteLabelValues(s.sub.Durable)
}

// halt is Close's stop: handlers' context ends at once, and nothing waits.
func (s *subscription) halt() {
	s.hardOnce.Do(func() { close(s.hard) })
	s.stopLoop()
}
