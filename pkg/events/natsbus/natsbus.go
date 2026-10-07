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
// It is the production bus. Streams, durable pull consumers with their
// dead-letter watchers, and key/value buckets are created from an
// events.Topology by Ensure (the manager's alone once the split lands, spec
// §5.9); Subscribe only binds what Ensure created, and Pull alone creates its
// own dynamic durable. Handler errors are translated into explicit
// acknowledgements, delayed negative acknowledgements and dead-letter copies
// by the shared events.Settle policy, a final delivery whose handler hangs
// past its acknowledgement deadline is dead-lettered from JetStream's
// MAX_DELIVERIES advisory, which every subscription watches for its own
// consumer, and each subscription runs up to MaxInFlight handlers, plus as
// many again while handlers are past their acknowledgement deadline, so its
// observable behaviour matches membus.
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// DefaultRequestTimeout bounds a Request that arrives with no context
// deadline of its own.
const DefaultRequestTimeout = 45 * time.Second

// jsStoreFailed is the JetStream API error code the server returns when a
// DiscardNew stream refuses a publish because it is at its limit.
const jsStoreFailed jetstream.ErrorCode = 10077

// settleTimeout bounds settling one delivery -- the acknowledgement, the
// negative acknowledgement or the dead-letter copy and termination -- once
// its handler has returned.
const settleTimeout = 10 * time.Second

type options struct {
	domain         string
	apiPrefix      string
	requestTimeout time.Duration
	hooks          events.Hooks
	serveLimits    events.ServeLimits
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

// WithServeLimits bounds each Serve call's handlers: how many run at once
// and how many requests wait for a slot before one is refused with
// events.ErrResponderBusy. A zero field takes its default
// (events.DefaultServeConcurrency, events.DefaultServeQueue).
func WithServeLimits(l events.ServeLimits) Option {
	return func(o *options) { o.serveLimits = l }
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

	mu           sync.Mutex
	closed       bool
	topology     events.Topology
	buckets      map[string]jetstream.KeyValue
	objectStores map[string]jetstream.ObjectStore
	responders   []*nats.Subscription
	subs         []*subscription

	// serveCtx parents every responder's handler context; Close cancels it,
	// so handlers still running or waiting for a slot stop with the bus.
	serveCtx    context.Context
	serveCancel context.CancelFunc
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
	serveCtx, serveCancel := context.WithCancel(context.Background())
	return &Bus{
		serveCtx:     serveCtx,
		serveCancel:  serveCancel,
		nc:           nc,
		js:           js,
		opts:         o,
		buckets:      map[string]jetstream.KeyValue{},
		objectStores: map[string]jetstream.ObjectStore{},
	}, nil
}

// JetStream exposes the underlying context for the few callers that need
// JetStream directly, such as the message replay path.
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
	responders, subs := b.responders, b.subs
	b.responders, b.subs = nil, nil
	b.mu.Unlock()
	b.serveCancel()

	for _, s := range subs {
		s.halt()
	}
	var errs []error
	for _, s := range responders {
		errs = append(errs, unsubscribe(s))
	}
	return errors.Join(errs...)
}

// unsubscribe removes a core-NATS subscription, treating one the connection
// has already dropped as removed.
func unsubscribe(s *nats.Subscription) error {
	if err := s.Unsubscribe(); err != nil &&
		!errors.Is(err, nats.ErrConnectionClosed) &&
		!errors.Is(err, nats.ErrBadSubscription) {
		return err
	}
	return nil
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

// Subscribe binds to the durable pull consumer sub names and starts consuming
// it. It never creates or updates consumer config, for the durable or for its
// dead-letter watcher: both are topology objects the manager's EnsureTopology
// creates (spec §5.9), so an older process cannot revert a newer topology,
// and sub's tuning other than MaxInFlight and Drain is the topology's, not the
// caller's: lapses, delayed naks and Settle read the bound durable's AckWait,
// Backoff and MaxDeliver, with a warning per bind when they differ from sub's
// (S5). It returns at once; the subscription binds the durable and its
// watcher when they exist, waiting while either is missing, and binds them
// again if they disappear while it runs, as a NATS restart wipes a
// memory-backed stream.
//
// Slots. Up to sub.MaxInFlight handlers run at once (one when it is unset),
// as on membus, each on its own goroutine, and the subscription fetches only
// for free slots: sub.MaxInFlight is this process's share, and the durable's
// MaxAckPending, the topology's, is the cap across every process. A handler
// past its delivery's acknowledgement deadline (events.AckDeadline) gives its
// slot back while fewer than MaxInFlight are past theirs, so a hung handler
// does not stop the next task, and at most twice MaxInFlight handlers ever
// run at once. See subscription.
//
// The stop function stops fetching, lets running handlers keep their context
// for up to sub.Drain, then cancels it and waits for them, as membus's does.
// Close cancels the handlers' context at once and does not wait.
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

	s := newSubscription(ctx, b, sub, func(hctx context.Context, eff events.Subscription, m jetstream.Msg, dh deliveryHooks) {
		b.handle(hctx, eff, h, m, dh)
	})
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.halt()
		return nil, events.ErrClosed
	}
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	s.start()
	return s.stop, nil
}

// receive wraps one delivery the way every consumer path must: decode the
// envelope, run Hooks.AfterReceive, and bind the message to its subscription.
// Subscribe's handle and Pull's Next share it, so a message looks identical
// whether a handler or a caller settles it.
//
// h.onProgress, when set, is called on every InProgress the handler sends, so
// the subscription's lapse deadline follows the server's, and h.lapsed mutes a
// lapsed delivery's InProgress and Nak; Pull passes the zero deliveryHooks.
func (b *Bus) receive(ctx context.Context, jm jetstream.Msg, sub events.Subscription,
	h deliveryHooks,
) (context.Context, *message, error) {
	msg := newMessage(jm, sub.Backoff, sub.Durable)
	msg.onProgress = h.onProgress
	msg.lapsed = h.lapsed
	hctx := b.opts.hooks.RunAfterReceive(ctx, msg.Envelope())
	return hctx, msg, nil
}

func (b *Bus) handle(ctx context.Context, sub events.Subscription,
	h events.Handler, jm jetstream.Msg, dh deliveryHooks,
) {
	hctx, msg, rerr := b.receive(ctx, jm, sub, dh)
	if rerr != nil {
		// receive cannot fail today (see its doc comment); if a future step
		// inside it can, leave the delivery unsettled for redelivery rather
		// than losing it or panicking the dispatch goroutine.
		logging.FromContext(ctx).Error("bus: receive failed; message left for redelivery",
			"durable", sub.Durable, "error", rerr)
		return
	}
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
	// Settle on a context of its own: the handler's may be cancelled by now
	// (a stop, or the service shutting down), and a handler that finished
	// its work must still get its acknowledgement to the server, or the
	// message is redelivered and the work done twice.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	s := events.Settle(err, msg.Attempt(), sub)
	if err != nil {
		// A handler that fails on every delivery is otherwise completely
		// silent: the message is naked and redelivered forever with nothing
		// written anywhere. That is how an illegal NATS KV key in the
		// metadata cache went undiagnosed until someone rebuilt the image
		// with a temporary print in this function. The error belongs in the
		// log at the moment it is settled, with what was decided about it.
		logging.FromContext(hctx).Error("bus: handler failed",
			"durable", sub.Durable,
			"schema", msg.Envelope().Schema,
			"msg_id", msg.Envelope().ID,
			"attempt", msg.Attempt(),
			"action", string(s.Action),
			"reason", s.Reason,
			"error", err)
	}
	switch s.Action {
	case events.SettleAck:
		_ = msg.Ack(sctx)
	case events.SettleNak:
		_ = msg.Nak(sctx, s.Delay)
	case events.SettleTerm:
		b.deadLetter(sctx, msg, sub.Durable, s.Reason)
		_ = msg.Term(sctx, s.Reason)
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
	if _, err := b.Publish(pubCtx, subject, env); err != nil {
		// The delivery is terminated regardless, so this log line is the
		// only record left of the message: say which one and why.
		logging.FromContext(ctx).Error("bus: dead-letter copy not stored; message terminated without one",
			"durable", durable,
			"msg_id", msg.Envelope().ID,
			"schema", msg.Envelope().Schema,
			"dlq_subject", subject,
			"reason", reason,
			"error", err)
	}
}

// Service error headers, as the NATS micro framework names them.
const (
	headerServiceError     = "Nats-Service-Error"
	headerServiceErrorCode = "Nats-Service-Error-Code"
)

// respondError answers a request with a header-only service error, the
// shape Request turns back into an *events.ResponderError on the caller's
// side. It carries no body, so it fits under any max_payload, and its text
// is folded onto one line, since a header value cannot carry a line break.
func respondError(m *nats.Msg, code int, msg string) {
	reply := &nats.Msg{Subject: m.Reply, Header: nats.Header{}}
	reply.Header.Set(headerServiceError, msg)
	reply.Header.Set(headerServiceErrorCode, strconv.Itoa(code))
	_ = m.RespondMsg(reply)
}

// Serve registers a queue-group responder on core NATS.
//
// nats.go calls a subscription's callback for one message at a time, so the
// callback never runs the handler and never blocks: it admits the request
// through the Serve's events.ServeGate, or refuses it at once with
// events.ErrResponderBusy, and hands it to a goroutine that waits for a
// handler slot. Up to the gate's limits run and wait at once; see
// pkg/events/serve.go for the rules and why.
//
// A handler's deadline is the caller's remaining time (events.HeaderTimeout,
// measured from receipt), capped at the bus's request timeout, which alone
// bounds a request that carries none. A request whose caller's time runs out
// before a slot frees is dropped unrun and unanswered. A handler that panics
// is logged with its stack and answered with events.ErrResponderFailed, and
// the responder keeps serving.
func (b *Bus) Serve(subject, queue string,
	h func(ctx context.Context, data []byte) ([]byte, error),
) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return events.ErrClosed
	}
	b.mu.Unlock()

	limits := b.opts.serveLimits.WithDefaults()
	gate := events.NewServeGate(limits)
	cb := func(m *nats.Msg) {
		var callerLeft time.Duration
		hasCaller := false
		if m.Header != nil {
			callerLeft, hasCaller = events.ParseTimeout(m.Header.Get(events.HeaderTimeout))
		}
		timeout, bounded, expired := events.HandlerTimeout(b.opts.requestTimeout, callerLeft, hasCaller)
		if expired {
			logging.FromContext(b.serveCtx).Debug("bus: request dropped: its caller's deadline had passed",
				"subject", m.Subject)
			return
		}
		leave, ok := gate.Admit()
		if !ok {
			logging.FromContext(b.serveCtx).Warn("bus: request refused: responder busy",
				"subject", m.Subject, "concurrency", limits.Concurrency, "queue", limits.Queue)
			respondError(m, events.ServiceErrorBusy, events.BusyMessage(limits))
			return
		}
		ctx, cancel := b.serveCtx, context.CancelFunc(func() {})
		if bounded {
			// Measured from now, receipt, so a request that waits for a
			// slot spends its caller's time waiting, not on top of it.
			ctx, cancel = context.WithTimeout(ctx, timeout)
		}
		go func() {
			defer leave()
			defer cancel()
			b.respond(ctx, gate, subject, m, h)
		}()
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

// respond waits for a handler slot, runs h on one admitted request and
// answers it.
func (b *Bus) respond(ctx context.Context, gate *events.ServeGate, subject string,
	m *nats.Msg, h func(ctx context.Context, data []byte) ([]byte, error),
) {
	release, err := gate.Acquire(ctx)
	if err != nil {
		// The caller has given up (or the bus closed) before a slot
		// freed: nobody is listening for an answer, so run nothing.
		logging.FromContext(ctx).Debug("bus: request dropped: no handler slot before its deadline",
			"subject", m.Subject, "error", err)
		return
	}
	defer release()

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

	data, stack, err := events.CallResponder(ctx, h, m.Data)
	if stack != nil {
		logging.FromContext(ctx).Error("bus: responder handler panicked",
			"subject", subject, "error", err, "stack", string(stack))
	}
	if err != nil {
		respondError(m, events.ServiceErrorFailed, events.OneLine(err))
		return
	}
	if err := m.Respond(data); err != nil {
		// The connection refused to send the reply -- nats.ErrMaxPayload
		// when the body is over the server's max_payload -- and the
		// requester would otherwise wait out its whole deadline with
		// nothing to say why (2026-09-24: a 1.3 MB .nzb against the
		// Helm chart's 1 MiB default stranded every usenet grab as
		// "context deadline exceeded"). A header-only error reply always
		// fits, so the requester fails at once and names the cause.
		logging.FromContext(ctx).Error("bus: reply not sent",
			"subject", subject, "bytes", len(data), "err", err)
		respondError(m, events.ServiceErrorFailed, events.OneLine(
			fmt.Errorf("natsbus: reply of %d bytes to %q not sent: %w", len(data), subject, err)))
	}
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
	msg := &nats.Msg{Subject: subject, Data: data, Header: nats.Header{}}
	if reqEnv.Trace != "" {
		msg.Header.Set(events.HeaderTrace, reqEnv.Trace)
	}
	// Tell the responder how long this caller will wait, so it neither
	// runs a handler past that nor starts one after it (Serve).
	if left, ok := events.FormatTimeout(ctx); ok {
		msg.Header.Set(events.HeaderTimeout, left)
	}

	reply, err := b.nc.RequestMsgWithContext(ctx, msg)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return fmt.Errorf("natsbus: %q: %w", subject, events.ErrNoResponders)
		}
		return fmt.Errorf("natsbus: request %q: %w", subject, err)
	}
	if msg := reply.Header.Get(headerServiceError); msg != "" {
		code, _ := strconv.Atoi(reply.Header.Get(headerServiceErrorCode))
		return fmt.Errorf("natsbus: %s: %w", subject,
			&events.ResponderError{Code: code, Message: msg})
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
