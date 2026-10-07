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

// Package membus is an in-process implementation of events.Bus.
//
// It exists so packages that publish or consume messages can be unit-tested
// without a broker, and it deliberately reproduces the observable semantics
// of natsbus rather than a simplified subset: work-queue claiming, explicit
// acknowledgement with redelivery after the ack deadline (Backoff[n-1] for
// delivery n when Backoff is set, as JetStream times it), delayed
// redelivery, MaxDeliver with a copy to the dead-letter stream (including a
// final delivery whose handler hangs past its ack deadline, which natsbus
// catches from JetStream's MAX_DELIVERIES advisory), publish
// deduplication by message ID inside the stream's duplicate window,
// DiscardNew back-pressure, scheduled publishes, per-subscription slots with
// lapse reclaim and drain, as natsbus, each durable's MaxAckPending across
// every subscription of it, and key/value create, compare-and-swap and TTL.
//
// It is not a durable store: everything lives in memory and dies with the
// process.
package membus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// pollInterval is how often a subscription looks for work it can claim. The
// in-memory bus has no server to push to it, so it polls; the interval is
// short enough that tests do not notice it and long enough that an idle bus
// costs nothing.
const pollInterval = 2 * time.Millisecond

// options is the resolved effect of a list of Option values.
type options struct {
	hooks       events.Hooks
	serveLimits events.ServeLimits
}

// Option configures the bus. It mirrors natsbus's functional-option style.
type Option func(*options)

// WithHooks installs the observability hooks called around every publish and
// receive. The default is the zero events.Hooks, which are no-ops.
func WithHooks(h events.Hooks) Option {
	return func(o *options) { o.hooks = h }
}

// WithServeLimits bounds each Serve call's handlers, as natsbus's option of
// the same name does. A zero field takes its default.
func WithServeLimits(l events.ServeLimits) Option {
	return func(o *options) { o.serveLimits = l }
}

// Bus is an in-process events.Bus.
type Bus struct {
	clock clockwork.Clock
	opts  options

	mu           sync.Mutex
	closed       bool
	topology     events.Topology
	streams      map[string]*stream
	buckets      map[string]*bucket
	objectStores map[string]*objectBucket
	responders   map[string][]*responder
	subs         map[*memSub]struct{} // running Subscribe calls, for Wedged

	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
}

var _ events.Bus = (*Bus)(nil)

// New returns an empty bus. Call Ensure before publishing: like JetStream,
// a subject with no stream behind it is an error, not a silent drop.
//
// clock may be nil, in which case a real clock is used. A fake clock makes
// redelivery, schedules and TTL deterministic, but note that the subscription
// loops poll, so a fake clock must be advanced from another goroutine.
func New(clock clockwork.Clock, opts ...Option) *Bus {
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	return &Bus{
		clock:        clock,
		opts:         o,
		streams:      map[string]*stream{},
		buckets:      map[string]*bucket{},
		objectStores: map[string]*objectBucket{},
		responders:   map[string][]*responder{},
		subs:         map[*memSub]struct{}{},
		done:         make(chan struct{}),
	}
}

// Ensure creates or updates the streams, consumers and buckets in t. Existing
// messages survive an update, and a changed retention policy is refused
// exactly as natsbus refuses it.
func (b *Bus) Ensure(_ context.Context, t events.Topology) error {
	if err := t.Validate(); err != nil {
		return fmt.Errorf("membus: invalid topology: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return events.ErrClosed
	}
	for _, spec := range t.Streams {
		if existing, ok := b.streams[spec.Name]; ok {
			if existing.spec.Retention != spec.Retention {
				return fmt.Errorf("membus: stream %s has retention %q, topology wants %q: %w",
					spec.Name, existing.spec.Retention, spec.Retention,
					events.ErrRetentionImmutable)
			}
			existing.mu.Lock()
			existing.spec = spec
			existing.mu.Unlock()
			continue
		}
		b.streams[spec.Name] = &stream{spec: spec, dedup: map[string]dedupRecord{}}
	}
	// natsbus's Ensure creates every topology consumer (events.EnsureTopology).
	// membus records each as existing, with the filters and the cap it
	// enforces, so StreamAdmin.Missing, Subscriptions and ConsumerState
	// answer as natsbus would. t.Validate has rejected a consumer whose
	// stream is not in t.Streams, and the loop above created every stream,
	// so the lookup is never nil.
	for _, c := range t.Consumers {
		b.streams[c.Stream].bindDurable(c.Name, c.Filters, c.MaxAckPending, c.Subscription().Timing())
	}
	for _, spec := range t.Buckets {
		if existing, ok := b.buckets[spec.Name]; ok {
			existing.mu.Lock()
			existing.spec = spec
			existing.mu.Unlock()
			continue
		}
		b.buckets[spec.Name] = &bucket{spec: spec, vals: map[string]*kvValue{}}
	}
	for _, spec := range t.ObjectStores {
		if existing, ok := b.objectStores[spec.Name]; ok {
			existing.mu.Lock()
			existing.spec = spec
			existing.mu.Unlock()
			continue
		}
		// created stamps the bucket's generation, as a stream's creation
		// time does on NATS (ObjectStore.Status).
		b.objectStores[spec.Name] = &objectBucket{spec: spec, objects: map[string]*memObject{}, created: b.clock.Now()}
	}
	b.topology = t
	return nil
}

// Close stops every subscription and responder and releases the streams.
func (b *Bus) Close() error {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		close(b.done)
	})
	b.wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, bk := range b.buckets {
		bk.closeWatchers()
	}
	return nil
}

func (b *Bus) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// Publish stores e on subject.
func (b *Bus) Publish(ctx context.Context, subject string, e *events.Envelope,
	opts ...events.PublishOption,
) (events.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return events.Receipt{}, err
	}
	o := events.ResolvePublishOptions(e, opts)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return events.Receipt{}, events.ErrClosed
	}
	spec, ok := b.topology.StreamForSubject(subject)
	if !ok {
		b.mu.Unlock()
		return events.Receipt{}, fmt.Errorf("membus: no stream for subject %q: %w",
			subject, events.ErrStreamNotFound)
	}
	st := b.streams[spec.Name]
	b.mu.Unlock()
	if st == nil {
		return events.Receipt{}, fmt.Errorf("membus: stream %s not ensured: %w",
			spec.Name, events.ErrStreamNotFound)
	}
	if o.ExpectStream != "" && o.ExpectStream != spec.Name {
		return events.Receipt{}, fmt.Errorf(
			"membus: subject %q resolves to stream %s, expected %s",
			subject, spec.Name, o.ExpectStream)
	}
	env := e.Clone()
	if env == nil {
		env = &events.Envelope{}
	}
	env.ID = o.MsgID
	if env.Time.IsZero() {
		env.Time = b.clock.Now().UTC()
	}
	b.opts.hooks.RunBeforePublish(ctx, env)
	return st.publish(b.clock.Now(), subject, env, o.ScheduleAt)
}

// KV binds to a bucket created by Ensure. An unknown bucket yields a KV whose
// every method returns ErrBucketNotFound, matching natsbus, which cannot
// discover the bucket is missing until the first call either.
func (b *Bus) KV(name string) events.KV {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bk, ok := b.buckets[name]; ok {
		return &kvHandle{bus: b, bucket: bk}
	}
	return &kvHandle{bus: b, name: name}
}

// ObjectStore binds to a bucket created by Ensure. An unknown bucket yields
// an ObjectStore whose every method returns ErrBucketNotFound, matching KV
// and matching natsbus, which cannot discover the bucket is missing until
// the first call either.
func (b *Bus) ObjectStore(name string) events.ObjectStore {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bk, ok := b.objectStores[name]; ok {
		return &objectHandle{bus: b, bucket: bk, name: name}
	}
	return &objectHandle{bus: b, name: name}
}

// Subscribe binds a durable consumer over an in-memory stream and delivers
// to h. Like natsbus's, it never creates the durable: Ensure binds every
// topology consumer, with its filters and cap, and Subscribe waits while its
// durable is not bound -- not ensured yet, or deleted -- and resumes once it
// is. It runs up to sub.MaxInFlight handlers at once, claiming a message only
// into a free slot; a handler past its delivery's acknowledgement deadline
// gives its slot back while fewer than sub.MaxInFlight are past theirs
// (memSub); and the stop function lets running handlers keep their context
// for up to sub.Drain before cancelling it and waiting for them. Close
// cancels it at once. Like natsbus's, it times lapses and settles on the
// bound durable's AckWait, Backoff and MaxDeliver, not sub's, warning once
// when they differ (S5).
func (b *Bus) Subscribe(ctx context.Context, sub events.Subscription,
	h events.Handler,
) (func(), error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	if b.isClosed() {
		return nil, events.ErrClosed
	}

	loopCtx, stopLoop := context.WithCancel(ctx)
	// Handlers outlive the loop by up to sub.Drain, so their context is not
	// the subscription's, though it keeps its values.
	hctx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	ms := &memSub{slots: max(sub.MaxInFlight, 1), sub: sub, eff: sub, running: map[*memDelivery]struct{}{}}
	b.mu.Lock()
	b.subs[ms] = struct{}{}
	b.mu.Unlock()
	var handlers sync.WaitGroup
	drained := make(chan struct{})
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		closing := b.consume(loopCtx, hctx, sub, h, ms, &handlers)
		ms.stop()
		if !closing && sub.Drain > 0 {
			b.awaitIdle(&handlers, sub.Drain)
		}
		cancelHandlers()
		close(drained)
		handlers.Wait()
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			stopLoop()
			<-drained
			handlers.Wait()
			b.mu.Lock()
			delete(b.subs, ms)
			b.mu.Unlock()
		})
	}, nil
}

// freeSlots is the free slots of every running Subscribe on stream's durable:
// membus's nearest equivalent of NumWaiting, a pull request open per free
// slot.
func (b *Bus) freeSlots(stream, durable string) int {
	b.mu.Lock()
	subs := make([]*memSub, 0, len(b.subs))
	for ms := range b.subs {
		if ms.sub.Stream == stream && ms.sub.Durable == durable {
			subs = append(subs, ms)
		}
	}
	b.mu.Unlock()
	n := 0
	for _, ms := range subs {
		ms.mu.Lock()
		if !ms.stopping {
			n += max(ms.slots-ms.live, 0)
		}
		ms.mu.Unlock()
	}
	return n
}

var _ events.WedgeReporter = (*Bus)(nil)

// Wedged implements events.WedgeReporter as natsbus does: it names every
// running subscription whose lapsed cap handlers that ignore their context
// have held past its handler budget plus its first-delivery deadline.
func (b *Bus) Wedged() error {
	b.mu.Lock()
	subs := make([]*memSub, 0, len(b.subs))
	for ms := range b.subs {
		subs = append(subs, ms)
	}
	b.mu.Unlock()
	now := b.clock.Now()
	var errs []error
	for _, ms := range subs {
		errs = append(errs, ms.wedged(now))
	}
	return errors.Join(errs...)
}

// boundStream is sub's stream once sub's durable is bound on it, else nil.
func (b *Bus) boundStream(sub events.Subscription) *stream {
	b.mu.Lock()
	st := b.streams[sub.Stream]
	b.mu.Unlock()
	if st == nil || !st.hasDurable(sub.Durable) {
		return nil
	}
	return st
}

var _ events.DeadLetterWatcher = (*Bus)(nil)

// WatchDeadLetters implements events.DeadLetterWatcher by sweeping sub's
// durable for lapsed final deliveries, the copy natsbus makes from the
// MAX_DELIVERIES advisory, until stop or Close.
func (b *Bus) WatchDeadLetters(ctx context.Context, sub events.Subscription) (func(), error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	if b.isClosed() {
		return nil, events.ErrClosed
	}
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ackWait := func(attempt uint64) time.Duration { return events.AckDeadline(sub, attempt) }
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer close(done)
		for {
			if st := b.boundStream(sub); st != nil {
				b.deadLetterLapsed(wctx, st, sub, ackWait)
			}
			select {
			case <-wctx.Done():
				return
			case <-b.done:
				return
			case <-b.clock.After(pollInterval):
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

// memSub is one Subscribe call's handler slots. A message is claimed only
// into a free slot, as natsbus fetches only for free slots, and a delivery
// past its acknowledgement deadline gives its slot back while fewer than
// slots are lapsed (spec §9.3). A budgeted delivery (HandlerTimeout) is
// heartbeaten, cancelled at its budget and one deadline after a lapse, and a
// lapsed cap held past the budget is a wedge, as on natsbus (S1). memSub.mu
// then stream.mu is the only order either is taken in.
type memSub struct {
	slots   int
	sub     events.Subscription
	mu      sync.Mutex
	eff     events.Subscription // sub with the bound durable's timing
	live    int
	lapsed  int
	running map[*memDelivery]struct{}
	// saturatedSince is when lapsed deliveries came to hold the lapsed cap,
	// zero while they do not; stopping is set once the loop has ended.
	saturatedSince time.Time
	stopping       bool
}

// memDelivery is one running handler's message and the attempt it runs.
type memDelivery struct {
	msg      *memMsg
	attempt  uint64
	lapsed   bool
	lapsedAt time.Time

	// cancel ends a budgeted delivery's handler context; nil without a
	// budget. cancelled records the ErrLapsed cancel.
	cancel    context.CancelCauseFunc
	cancelled bool
}

func (s *memSub) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live >= s.slots {
		return false
	}
	s.live++
	return true
}

func (s *memSub) unacquire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live--
}

func (s *memSub) track(m *memMsg, attempt uint64) *memDelivery {
	d := &memDelivery{msg: m, attempt: attempt}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[d] = struct{}{}
	return d
}

// isLapsed reports whether d has given its slot back: its InProgress and Nak
// are muted then, as natsbus mutes them (split §9.3 as amended 2026-10-07, S2).
func (s *memSub) isLapsed(d *memDelivery) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return d.lapsed
}

func (s *memSub) finish(d *memDelivery, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, d)
	if d.lapsed {
		s.lapsed--
	} else {
		s.live--
	}
	s.saturationLocked(now)
}

// reclaim marks running deliveries past their deadline, or made again since,
// lapsed, freeing their slots, while fewer than slots are lapsed, and cancels
// a budgeted lapsed delivery's handler one deadline after its lapse.
func (s *memSub) reclaim(st *stream, durable string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for d := range s.running {
		if d.lapsed {
			if d.cancel != nil && !d.cancelled && now.After(d.lapsedAt.Add(events.AckDeadline(s.eff, d.attempt))) {
				d.cancel(events.ErrLapsed)
				d.cancelled = true
			}
			continue
		}
		if s.lapsed >= s.slots || !st.overdue(d.msg, durable, d.attempt, now) {
			continue
		}
		d.lapsed = true
		d.lapsedAt = now
		s.live--
		s.lapsed++
	}
	s.saturationLocked(now)
}

// saturationLocked keeps saturatedSince current. The caller holds s.mu.
func (s *memSub) saturationLocked(now time.Time) {
	switch {
	case s.lapsed < s.slots:
		s.saturatedSince = time.Time{}
	case s.saturatedSince.IsZero():
		s.saturatedSince = now
	}
}

// setEffective records sub with the bound durable's timing.
func (s *memSub) setEffective(eff events.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eff = eff
}

func (s *memSub) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
}

// wedged is natsbus's subscription.wedged on membus's clock.
func (s *memSub) wedged(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.saturatedSince.IsZero() {
		return nil
	}
	budget := cmp.Or(s.sub.HandlerTimeout, s.sub.AckWait, events.DefaultAckWait)
	limit := budget + events.AckDeadline(s.eff, 1)
	if held := now.Sub(s.saturatedSince); held > limit {
		return fmt.Errorf("bus: every slot of %s has been held by lapsed handlers for %v (limit %v): they ignore their context",
			s.sub.Durable, held.Round(time.Second), limit)
	}
	return nil
}

// budget gives d's handler, when sub has a HandlerTimeout, a context
// cancelled with events.ErrHandlerBudget at the budget and a heartbeat that
// keeps its claim's deadline every third of it until the delivery lapses, on
// the bus's clock. The returned func ends both once the handler returns.
func (b *Bus) budget(ctx context.Context, ms *memSub, d *memDelivery, st *stream,
	eff events.Subscription, ackWait func(uint64) time.Duration,
) (context.Context, func()) {
	if ms.sub.HandlerTimeout <= 0 {
		return ctx, func() {}
	}
	hctx, cancel := context.WithCancelCause(ctx)
	ms.mu.Lock()
	d.cancel = cancel
	ms.mu.Unlock()
	done := make(chan struct{})
	budget := b.clock.After(ms.sub.HandlerTimeout)
	every := max(events.AckDeadline(eff, d.attempt)/3, pollInterval)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-hctx.Done():
				return
			case <-b.done:
				return
			case <-budget:
				cancel(events.ErrHandlerBudget)
				return
			case <-b.clock.After(every):
				if ms.isLapsed(d) {
					continue // muted, as natsbus stops beating; the budget still runs
				}
				st.inProgress(d.msg, eff.Durable, b.clock.Now(), ackWait)
			}
		}
	}()
	return hctx, func() { close(done); cancel(nil) }
}

// consume claims into free slots, once sub's durable is bound, until ctx
// ends (false) or the bus closes (true). Lapsed final deliveries are swept on
// every pass, with or without a free slot: JetStream gives up on them
// whatever the client is doing. Every pass reads the bound durable's timing.
func (b *Bus) consume(ctx, hctx context.Context, sub events.Subscription, h events.Handler,
	ms *memSub, handlers *sync.WaitGroup,
) bool {
	warnedSkew := false
	for {
		select {
		case <-ctx.Done():
			return false
		case <-b.done:
			return true
		default:
		}
		st := b.boundStream(sub)
		if st == nil { // not created yet, or deleted: wait, as natsbus does
			select {
			case <-ctx.Done():
				return false
			case <-b.done:
				return true
			case <-b.clock.After(pollInterval):
			}
			continue
		}
		eff := sub
		if bound, ok := st.boundTiming(sub.Durable); ok {
			eff = sub.WithTiming(bound)
			if declared := sub.Timing(); !warnedSkew && !declared.Equal(bound) {
				warnedSkew = true
				logging.FromContext(ctx).Warn("bus: the durable's timing differs from this process's; using the durable's (version skew?)",
					"stream", sub.Stream, "durable", sub.Durable,
					"declared", fmt.Sprintf("%+v", declared), "bound", fmt.Sprintf("%+v", bound))
			}
		}
		ms.setEffective(eff)
		ackWait := func(attempt uint64) time.Duration { return events.AckDeadline(eff, attempt) }
		now := b.clock.Now()
		b.deadLetterLapsed(ctx, st, eff, ackWait)
		ms.reclaim(st, sub.Durable, now)
		if ms.acquire() {
			if m := st.claim(sub.Durable, sub.Filters, now, ackWait, eff.MaxDeliver); m != nil {
				d := ms.track(m, st.attemptOf(m, sub.Durable))
				dctx, endBudget := b.budget(hctx, ms, d, st, eff, ackWait)
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					defer func() { ms.finish(d, b.clock.Now()) }()
					defer endBudget()
					b.deliver(dctx, st, eff, h, m, ackWait, func() bool { return ms.isLapsed(d) })
				}()
				continue
			}
			ms.unacquire()
		}
		select {
		case <-ctx.Done():
			return false
		case <-b.done:
			return true
		case <-b.clock.After(pollInterval):
		}
	}
}

// awaitIdle waits up to d for every handler to return.
func (b *Bus) awaitIdle(handlers *sync.WaitGroup, d time.Duration) {
	idle := make(chan struct{})
	go func() { handlers.Wait(); close(idle) }()
	select {
	case <-idle:
	case <-b.clock.After(d):
	case <-b.done:
	}
}

// claimNext sweeps st for sub's lapsed final deliveries and then claims the
// next deliverable message, if any, for Pull's Next (pull.go). Subscribe's
// consume makes the same two calls around its slots.
func (b *Bus) claimNext(ctx context.Context, st *stream, sub events.Subscription,
	ackWait func(attempt uint64) time.Duration,
) *memMsg {
	b.deadLetterLapsed(ctx, st, sub, ackWait)
	return st.claim(sub.Durable, sub.Filters, b.clock.Now(), ackWait, sub.MaxDeliver)
}

// deadLetterLapsed copies every message whose final delivery to sub has
// lapsed to the dead-letter stream, without a handler slot and without the
// handler returning. It is membus's equivalent of natsbus's MAX_DELIVERIES
// advisory watcher: a handler still hung when the last acknowledgement
// deadline passes is otherwise never settled, and nothing would ever copy the
// message. The copy carries the ID the in-process path would give it, so a
// hung handler that finally returns an error is a duplicate, not a second
// copy.
func (b *Bus) deadLetterLapsed(ctx context.Context, st *stream, sub events.Subscription,
	ackWait func(attempt uint64) time.Duration,
) {
	for _, m := range st.lapsed(sub.Durable, sub.Filters, b.clock.Now(), sub.MaxDeliver) {
		msg := &message{bus: b, stream: st, msg: m, durable: sub.Durable, ackWait: ackWait}
		b.deadLetter(ctx, msg, sub.Durable, events.AckWaitExhaustedReason(uint64(sub.MaxDeliver)))
	}
}

// wrapDelivery binds a claimed message to its subscription and runs
// Hooks.AfterReceive, the way every consumer path must: deliver's handler
// dispatch and Pull's Next (pull.go) share it, so a message looks identical
// whether a handler or a caller settles it.
func (b *Bus) wrapDelivery(ctx context.Context, st *stream, sub events.Subscription,
	m *memMsg, ackWait func(attempt uint64) time.Duration,
) (context.Context, *message) {
	msg := &message{bus: b, stream: st, msg: m, durable: sub.Durable, ackWait: ackWait}
	hctx := b.opts.hooks.RunAfterReceive(ctx, msg.Envelope())
	return hctx, msg
}

// deliver runs one handler invocation and settles the message. lapsed reports
// that the delivery has given its slot back, which mutes its InProgress and
// Nak.
func (b *Bus) deliver(ctx context.Context, st *stream, sub events.Subscription,
	h events.Handler, m *memMsg, ackWait func(attempt uint64) time.Duration,
	lapsed func() bool,
) {
	hctx, msg := b.wrapDelivery(ctx, st, sub, m, ackWait)
	msg.lapsed = lapsed
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = events.Discard(fmt.Sprintf("handler panicked: %v", r), nil)
			}
		}()
		err = h(hctx, msg)
	}()
	if msg.settledByHandler() {
		return
	}
	b.settle(ctx, msg, sub, events.Settle(err, msg.Attempt(), sub))
}

func (b *Bus) settle(ctx context.Context, msg *message, sub events.Subscription,
	s events.Settlement,
) {
	switch s.Action {
	case events.SettleAck:
		_ = msg.Ack(ctx)
	case events.SettleNak:
		_ = msg.Nak(ctx, s.Delay)
	case events.SettleTerm:
		b.deadLetter(ctx, msg, sub.Durable, s.Reason)
		_ = msg.Term(ctx, s.Reason)
	}
}

// deadLetter copies the message to the dead-letter stream before the delivery
// is terminated. A failure to publish the copy is not fatal: terminating
// anyway is better than redelivering a message the handler has refused. It is
// logged, as natsbus logs it, since that line is then the only record left.
func (b *Bus) deadLetter(ctx context.Context, msg *message, durable, reason string) {
	subject, env := events.DeadLetter(msg, durable, reason)
	pubCtx := ctx
	if pubCtx.Err() != nil {
		pubCtx = context.Background()
	}
	if _, err := b.Publish(pubCtx, subject, env); err != nil {
		logging.FromContext(ctx).Error("bus: dead-letter copy not stored; message terminated without one",
			"durable", durable,
			"msg_id", msg.Envelope().ID,
			"schema", msg.Envelope().Schema,
			"dlq_subject", subject,
			"reason", reason,
			"error", err)
	}
}

// Serve registers an in-process responder.
func (b *Bus) Serve(subject, queue string,
	h func(ctx context.Context, data []byte) ([]byte, error),
) error {
	if subject == "" {
		return fmt.Errorf("membus: %w", events.ErrNoResponders)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return events.ErrClosed
	}
	limits := b.opts.serveLimits.WithDefaults()
	b.responders[subject] = append(b.responders[subject], &responder{
		subject: subject, queue: queue, handle: h,
		limits: limits, gate: events.NewServeGate(limits), done: b.done,
	})
	return nil
}

// Request calls a responder and decodes its single reply.
//
// A request is a publish, so it runs BeforePublish exactly like Publish
// does: a hook that stamps a trace (tracing.Inject) puts it on the request
// envelope, and call runs AfterReceive on that envelope before starting the
// responder's handler -- see responder.call. The reply is deliberately NOT
// run through BeforePublish: see natsbus.Bus.Request's doc comment, which
// this mirrors, for why request/reply's synchronous round trip has no
// second hop for a hook to bridge on the way back.
func (b *Bus) Request(ctx context.Context, subject string, in, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return events.ErrClosed
	}
	r := pickResponder(b.responders, subject)
	b.mu.Unlock()
	if r == nil {
		return fmt.Errorf("membus: %q: %w", subject, events.ErrNoResponders)
	}
	reqEnv := &events.Envelope{}
	b.opts.hooks.RunBeforePublish(ctx, reqEnv)
	return r.call(ctx, reqEnv, b.opts.hooks, in, out)
}

func pickResponder(all map[string][]*responder, subject string) *responder {
	if rs := all[subject]; len(rs) > 0 {
		return rs[0]
	}
	for filter, rs := range all {
		if len(rs) > 0 && strings.ContainsAny(filter, "*>") &&
			events.SubjectMatches(filter, subject) {
			return rs[0]
		}
	}
	return nil
}
