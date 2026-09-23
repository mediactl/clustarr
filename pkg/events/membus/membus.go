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
// DiscardNew back-pressure, scheduled publishes, and key/value create,
// compare-and-swap and TTL.
//
// It is not a durable store: everything lives in memory and dies with the
// process.
package membus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/mediactl/clustarr/pkg/events"
)

// pollInterval is how often a subscription looks for work it can claim. The
// in-memory bus has no server to push to it, so it polls; the interval is
// short enough that tests do not notice it and long enough that an idle bus
// costs nothing.
const pollInterval = 2 * time.Millisecond

// options is the resolved effect of a list of Option values.
type options struct {
	hooks events.Hooks
}

// Option configures the bus. It mirrors natsbus's functional-option style.
type Option func(*options)

// WithHooks installs the observability hooks called around every publish and
// receive. The default is the zero events.Hooks, which are no-ops.
func WithHooks(h events.Hooks) Option {
	return func(o *options) { o.hooks = h }
}

// Bus is an in-process events.Bus.
type Bus struct {
	clock clockwork.Clock
	opts  options

	mu         sync.Mutex
	closed     bool
	topology   events.Topology
	streams    map[string]*stream
	buckets    map[string]*bucket
	responders map[string][]*responder

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
		clock:      clock,
		opts:       o,
		streams:    map[string]*stream{},
		buckets:    map[string]*bucket{},
		responders: map[string][]*responder{},
		done:       make(chan struct{}),
	}
}

// Ensure creates or updates the streams and buckets in t. Existing messages
// survive an update, and a changed retention policy is refused exactly as
// natsbus refuses it.
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
	for _, spec := range t.Buckets {
		if existing, ok := b.buckets[spec.Name]; ok {
			existing.mu.Lock()
			existing.spec = spec
			existing.mu.Unlock()
			continue
		}
		b.buckets[spec.Name] = &bucket{spec: spec, vals: map[string]*kvValue{}}
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

// Subscribe starts a durable consumer over an in-memory stream.
func (b *Bus) Subscribe(ctx context.Context, sub events.Subscription,
	h events.Handler,
) (func(), error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, events.ErrClosed
	}
	st := b.streams[sub.Stream]
	b.mu.Unlock()
	if st == nil {
		return nil, fmt.Errorf("membus: stream %s not ensured: %w",
			sub.Stream, events.ErrStreamNotFound)
	}

	loopCtx, cancel := context.WithCancel(ctx)
	inFlight := sub.MaxInFlight
	if inFlight <= 0 {
		inFlight = 1
	}
	ackWait := func(attempt uint64) time.Duration { return ackWaitFor(sub, attempt) }

	var handlers sync.WaitGroup
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer handlers.Wait()
		sem := make(chan struct{}, inFlight)
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-b.done:
				return
			default:
			}
			m := b.claimNext(loopCtx, st, sub, ackWait)
			if m == nil {
				select {
				case <-loopCtx.Done():
				case <-b.done:
				case <-b.clock.After(pollInterval):
				}
				continue
			}
			// Wait for a free handler slot. Every slot may be held by a
			// handler that has hung, so keep sweeping while waiting:
			// JetStream expires a final delivery on the server whatever the
			// client is doing, and so must this bus.
		wait:
			for {
				select {
				case sem <- struct{}{}:
					break wait
				case <-loopCtx.Done():
					return
				case <-b.done:
					return
				case <-b.clock.After(pollInterval):
					b.deadLetterLapsed(loopCtx, st, sub, ackWait)
				}
			}
			handlers.Add(1)
			go func(m *memMsg) {
				defer handlers.Done()
				defer func() { <-sem }()
				b.deliver(loopCtx, st, sub, h, m, ackWait)
			}(m)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			handlers.Wait()
		})
	}, nil
}

// defaultAckWait is the acknowledgement deadline of a subscription that
// sets neither AckWait nor Backoff: JetStream's own default
// (nats-server JsAckWaitDefault).
const defaultAckWait = 30 * time.Second

// ackWaitFor is how long delivery attempt (1-based) of sub may go unsettled
// before the message is due again. It is JetStream's rule, not a membus
// choice: with Backoff set, nats-server overrides AckWait with Backoff[0] and
// times delivery n out on Backoff[n-1], reusing the last entry past the end
// (consumer.go checkPending: deadline = BackOff[rdc], rdc = deliveries-1);
// without Backoff, every delivery gets AckWait. An InProgress resets the
// clock but keeps the delivery's deadline.
func ackWaitFor(sub events.Subscription, attempt uint64) time.Duration {
	if n := len(sub.Backoff); n > 0 {
		i := 0
		if attempt > 1 {
			i = int(min(attempt-1, uint64(n-1)))
		}
		return sub.Backoff[i]
	}
	if sub.AckWait > 0 {
		return sub.AckWait
	}
	return defaultAckWait
}

// claimNext sweeps st for sub's lapsed final deliveries and then claims the
// next deliverable message, if any. Subscribe's delivery loop and Pull's Next
// (pull.go) share this so a claim behaves identically whether a handler or a
// caller settles the result; do not copy the two calls separately.
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

// deliver runs one handler invocation and settles the message.
func (b *Bus) deliver(ctx context.Context, st *stream, sub events.Subscription,
	h events.Handler, m *memMsg, ackWait func(attempt uint64) time.Duration,
) {
	msg := &message{bus: b, stream: st, msg: m, durable: sub.Durable, ackWait: ackWait}
	hctx := b.opts.hooks.RunAfterReceive(ctx, msg.Envelope())
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
// anyway is better than redelivering a message the handler has refused.
func (b *Bus) deadLetter(ctx context.Context, msg *message, durable, reason string) {
	subject, env := events.DeadLetter(msg, durable, reason)
	pubCtx := ctx
	if pubCtx.Err() != nil {
		pubCtx = context.Background()
	}
	_, _ = b.Publish(pubCtx, subject, env)
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
	b.responders[subject] = append(b.responders[subject],
		&responder{subject: subject, queue: queue, handle: h})
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
