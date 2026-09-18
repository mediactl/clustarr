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

// Package natsbus implements events.Bus on NATS JetStream.
//
// It is the production bus. Streams, durable pull consumers and key/value
// buckets are created from an events.Topology by Ensure; handler errors are
// translated into explicit acknowledgements, delayed negative
// acknowledgements and dead-letter copies by the shared events.Settle policy,
// so its observable behaviour matches membus.
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

// DefaultRequestTimeout bounds a Request that arrives with no context
// deadline of its own.
const DefaultRequestTimeout = 45 * time.Second

// jsStoreFailed is the JetStream API error code the server returns when a
// DiscardNew stream refuses a publish because it is at its limit.
const jsStoreFailed jetstream.ErrorCode = 10077

type options struct {
	domain         string
	apiPrefix      string
	requestTimeout time.Duration
	hooks          events.Hooks
}

// Option configures the bus.
type Option func(*options)

// WithDomain binds to JetStream in a leaf-node domain.
func WithDomain(d string) Option {
	return func(o *options) { o.domain = d }
}

// WithAPIPrefix binds to JetStream behind a non-default API prefix.
func WithAPIPrefix(p string) Option {
	return func(o *options) { o.apiPrefix = p }
}

// WithRequestTimeout sets the fallback deadline for Request calls whose
// context carries none.
func WithRequestTimeout(d time.Duration) Option {
	return func(o *options) { o.requestTimeout = d }
}

// WithHooks installs the observability hooks called around every publish and
// receive. The default is the zero events.Hooks, which are no-ops.
func WithHooks(h events.Hooks) Option {
	return func(o *options) { o.hooks = h }
}

// Bus is a JetStream-backed events.Bus.
type Bus struct {
	nc   *nats.Conn
	js   jetstream.JetStream
	opts options

	mu         sync.Mutex
	closed     bool
	topology   events.Topology
	buckets    map[string]jetstream.KeyValue
	responders []*nats.Subscription
	consumers  []jetstream.ConsumeContext
}

var _ events.Bus = (*Bus)(nil)

// New wraps a live NATS connection. It does not create anything: call Ensure
// with a topology before publishing or subscribing.
func New(nc *nats.Conn, opts ...Option) (*Bus, error) {
	if nc == nil {
		return nil, errors.New("natsbus: nil connection")
	}
	o := options{requestTimeout: DefaultRequestTimeout}
	for _, fn := range opts {
		fn(&o)
	}
	var (
		js  jetstream.JetStream
		err error
	)
	switch {
	case o.domain != "":
		js, err = jetstream.NewWithDomain(nc, o.domain)
	case o.apiPrefix != "":
		js, err = jetstream.NewWithAPIPrefix(nc, o.apiPrefix)
	default:
		js, err = jetstream.New(nc)
	}
	if err != nil {
		return nil, fmt.Errorf("natsbus: open jetstream: %w", err)
	}
	return &Bus{
		nc:      nc,
		js:      js,
		opts:    o,
		buckets: map[string]jetstream.KeyValue{},
	}, nil
}

// JetStream exposes the underlying context for the few callers that need
// JetStream directly, such as the dead-letter advisory watcher and the
// message replay path.
func (b *Bus) JetStream() jetstream.JetStream { return b.js }

// Ensure applies t and remembers it, so Publish can resolve a subject to its
// stream without a round trip.
func (b *Bus) Ensure(ctx context.Context, t events.Topology) error {
	if err := events.EnsureTopology(ctx, b.js, t); err != nil {
		return err
	}
	b.mu.Lock()
	b.topology = t
	b.mu.Unlock()
	return nil
}

// Close stops every subscription and responder. It does not close the NATS
// connection, which the caller owns.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	responders, consumers := b.responders, b.consumers
	b.responders, b.consumers = nil, nil
	b.mu.Unlock()

	for _, c := range consumers {
		c.Stop()
	}
	var errs []error
	for _, s := range responders {
		if err := s.Unsubscribe(); err != nil &&
			!errors.Is(err, nats.ErrConnectionClosed) &&
			!errors.Is(err, nats.ErrBadSubscription) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Publish stores e on subject and waits for the server acknowledgement.
func (b *Bus) Publish(ctx context.Context, subject string, e *events.Envelope,
	opts ...events.PublishOption,
) (events.Receipt, error) {
	b.mu.Lock()
	closed := b.closed
	top := b.topology
	b.mu.Unlock()
	if closed {
		return events.Receipt{}, events.ErrClosed
	}

	o := events.ResolvePublishOptions(e, opts)
	env := e.Clone()
	if env == nil {
		env = &events.Envelope{}
	}
	env.ID = o.MsgID
	if env.Time.IsZero() {
		env.Time = time.Now().UTC()
	}
	b.opts.hooks.RunBeforePublish(ctx, env)

	msg := &nats.Msg{Subject: subject, Data: env.Data, Header: nats.Header{}}
	for k, v := range env.ToHeaders() {
		msg.Header.Set(k, v)
	}

	var popts []jetstream.PublishOpt
	if o.MsgID != "" {
		popts = append(popts, jetstream.WithMsgID(o.MsgID))
	}
	if o.ExpectStream != "" {
		popts = append(popts, jetstream.WithExpectStream(o.ExpectStream))
	}
	if !o.ScheduleAt.IsZero() {
		hold, err := events.ScheduleSubject(subject)
		if err != nil {
			return events.Receipt{}, err
		}
		msg.Subject = hold
		popts = append(popts,
			jetstream.WithScheduleTarget(subject),
			jetstream.WithScheduleAt(o.ScheduleAt))
	}

	ack, err := b.js.PublishMsg(ctx, msg, popts...)
	if err != nil {
		return events.Receipt{}, publishError(top, subject, err)
	}
	return events.Receipt{Stream: ack.Stream, Seq: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// publishError maps JetStream publish failures onto the package sentinels the
// rest of Clustarr switches on.
func publishError(top events.Topology, subject string, err error) error {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode == jsStoreFailed {
		return fmt.Errorf("natsbus: publish %q: %s: %w",
			subject, apiErr.Description, events.ErrQueueFull)
	}
	if errors.Is(err, jetstream.ErrNoStreamResponse) {
		if _, ok := top.StreamForSubject(subject); !ok {
			return fmt.Errorf("natsbus: no stream for subject %q: %w",
				subject, events.ErrStreamNotFound)
		}
	}
	return fmt.Errorf("natsbus: publish %q: %w", subject, err)
}

// Subscribe creates or updates the durable pull consumer described by sub and
// starts consuming.
func (b *Bus) Subscribe(ctx context.Context, sub events.Subscription,
	h events.Handler,
) (func(), error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, events.ErrClosed
	}

	cfg := events.ConsumerConfig(events.ConsumerSpec{
		Name:          sub.Durable,
		Stream:        sub.Stream,
		Filters:       sub.Filters,
		AckWait:       sub.AckWait,
		MaxDeliver:    sub.MaxDeliver,
		BackOff:       sub.Backoff,
		MaxAckPending: sub.MaxInFlight,
		Heartbeat:     sub.Heartbeat,
	})
	cons, err := b.js.CreateOrUpdateConsumer(ctx, sub.Stream, cfg)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, fmt.Errorf("natsbus: stream %s: %w",
				sub.Stream, events.ErrStreamNotFound)
		}
		return nil, fmt.Errorf("natsbus: consumer %s on %s: %w",
			sub.Durable, sub.Stream, err)
	}

	var copts []jetstream.PullConsumeOpt
	if sub.MaxInFlight > 0 {
		copts = append(copts, jetstream.PullMaxMessages(sub.MaxInFlight))
	}
	cctx, err := cons.Consume(func(m jetstream.Msg) {
		b.handle(ctx, sub, h, m)
	}, copts...)
	if err != nil {
		return nil, fmt.Errorf("natsbus: consume %s: %w", sub.Durable, err)
	}

	b.mu.Lock()
	b.consumers = append(b.consumers, cctx)
	b.mu.Unlock()

	var once sync.Once
	return func() { once.Do(cctx.Stop) }, nil
}

func (b *Bus) handle(ctx context.Context, sub events.Subscription,
	h events.Handler, jm jetstream.Msg,
) {
	msg := newMessage(jm)
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
	s := events.Settle(err, msg.Attempt(), sub)
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

// deadLetter copies the message to CLUSTARR_DLQ before the delivery is
// terminated. If the copy cannot be stored the delivery is still terminated:
// redelivering a message a handler has refused would loop forever.
func (b *Bus) deadLetter(ctx context.Context, msg events.Message, durable, reason string) {
	subject, env := events.DeadLetter(msg, durable, reason)
	pubCtx := ctx
	if pubCtx.Err() != nil {
		var cancel context.CancelFunc
		pubCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	_, _ = b.Publish(pubCtx, subject, env)
}

// Serve registers a queue-group responder on core NATS.
func (b *Bus) Serve(subject, queue string,
	h func(ctx context.Context, data []byte) ([]byte, error),
) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return events.ErrClosed
	}
	b.mu.Unlock()

	cb := func(m *nats.Msg) {
		ctx := context.Background()
		if b.opts.requestTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, b.opts.requestTimeout)
			defer cancel()
		}
		// AfterReceive mirrors the subscribe path (see handle): extract
		// whatever Request's BeforePublish call stamped in HeaderTrace, so
		// the handler's own spans and any outbound provider calls it makes
		// are children of the caller's trace rather than orphaned roots.
		reqEnv := &events.Envelope{}
		if m.Header != nil {
			if tp := m.Header.Get(events.HeaderTrace); tp != "" {
				reqEnv.Trace = tp
			}
		}
		ctx = b.opts.hooks.RunAfterReceive(ctx, reqEnv)

		data, err := h(ctx, m.Data)
		if err != nil {
			reply := &nats.Msg{Subject: m.Reply, Header: nats.Header{}}
			reply.Header.Set("Nats-Service-Error", err.Error())
			reply.Header.Set("Nats-Service-Error-Code", "500")
			_ = m.RespondMsg(reply)
			return
		}
		_ = m.Respond(data)
	}

	var (
		sub *nats.Subscription
		err error
	)
	if queue == "" {
		sub, err = b.nc.Subscribe(subject, cb)
	} else {
		sub, err = b.nc.QueueSubscribe(subject, queue, cb)
	}
	if err != nil {
		return fmt.Errorf("natsbus: serve %q: %w", subject, err)
	}
	b.mu.Lock()
	b.responders = append(b.responders, sub)
	b.mu.Unlock()
	return nil
}

// Request sends in to subject and decodes the single reply into out.
//
// The request runs BeforePublish, exactly like Publish: a hook that stamps a
// trace (tracing.Inject) puts it in the wire request's HeaderTrace, and
// Serve's callback runs AfterReceive to pick it back up before its handler
// runs. The REPLY does not run BeforePublish, deliberately: unlike a
// fire-and-forget publish, a request/reply round trip is synchronous from
// the caller's point of view -- Request returns into the very ctx (and
// span) the caller already holds, so there is no second, independent hop
// for a hook to bridge on the way back, and nothing on the receiving end
// would ever read a trace header stamped on the reply.
func (b *Bus) Request(ctx context.Context, subject string, in, out any) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return events.ErrClosed
	}

	var data []byte
	switch v := in.(type) {
	case nil:
	case []byte:
		data = v
	default:
		enc, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("natsbus: encode request for %s: %w", subject, err)
		}
		data = enc
	}

	if _, ok := ctx.Deadline(); !ok && b.opts.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.opts.requestTimeout)
		defer cancel()
	}

	reqEnv := &events.Envelope{}
	b.opts.hooks.RunBeforePublish(ctx, reqEnv)
	msg := &nats.Msg{Subject: subject, Data: data}
	if reqEnv.Trace != "" {
		msg.Header = nats.Header{}
		msg.Header.Set(events.HeaderTrace, reqEnv.Trace)
	}

	reply, err := b.nc.RequestMsgWithContext(ctx, msg)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return fmt.Errorf("natsbus: %q: %w", subject, events.ErrNoResponders)
		}
		return fmt.Errorf("natsbus: request %q: %w", subject, err)
	}
	if msg := reply.Header.Get("Nats-Service-Error"); msg != "" {
		return fmt.Errorf("natsbus: %s: %s", subject, msg)
	}
	switch v := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*v = reply.Data
		return nil
	default:
		if err := json.Unmarshal(reply.Data, out); err != nil {
			return fmt.Errorf("natsbus: decode reply from %s: %w", subject, err)
		}
		return nil
	}
}
