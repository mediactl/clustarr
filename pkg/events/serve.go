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

package events

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// The request/reply rules every bus shares. They live here, not in each
// bus, so natsbus and membus cannot drift apart on them.
//
// A responder runs up to ServeLimits.Concurrency handlers at once per Serve
// call, each on its own goroutine. Before 2026-10-01 natsbus ran the handler
// inside the nats.go subscription callback, which nats.go calls serially, so
// every RPC verb was answered one request at a time per replica: index's
// one replica searched one query at a time while sixteen search consumers
// waited on it, and a request queued behind a slow one started after its
// caller had given up, so the caller retried and the queue only grew.
//
// A request that finds every slot busy waits for one, up to
// ServeLimits.Queue requests per Serve; past that it is answered at once
// with ErrResponderBusy, a header-only reply on NATS, so the caller fails
// fast instead of waiting out its deadline for nothing. A queue rather than
// an immediate refusal, because callers' bursts are routinely a little
// above the concurrency (several catalog replicas' search consumers
// against one index), and a short wait inside the caller's own deadline
// is cheaper than a failed RPC and a JetStream backoff. Concurrency is not
// a rate limit: callers own rate limiting per host (CLAUDE.md, "The caller
// owns rate limiting"), so the bound here only caps goroutines and memory.
//
// A request waits, and its handler runs, no longer than its caller will
// wait: Request stamps HeaderTimeout with the caller's remaining time, and
// the responder caps its handler's deadline at that, measured from receipt.
// A request whose caller's time has run out before a slot frees is dropped
// without running and without a reply: nobody is listening for one.

// HeaderTimeout carries a request's remaining time, in whole milliseconds,
// when the requester sent it. It is a duration rather than an absolute
// deadline, as gRPC's grpc-timeout is, so clock skew between the requester's
// node and the responder's cannot move it. The responder measures it from
// the moment it receives the request.
const HeaderTimeout = "Clustarr-Timeout"

// Service error codes a responder answers with, in the
// Nats-Service-Error-Code header on NATS. Request maps each back onto its
// sentinel.
const (
	// ServiceErrorFailed is a handler that returned an error or panicked,
	// or a reply the transport refused to send: ErrResponderFailed.
	ServiceErrorFailed = 500

	// ServiceErrorBusy is a responder whose handler slots and queue were
	// all taken: ErrResponderBusy.
	ServiceErrorBusy = 503
)

// DefaultServeConcurrency is how many handlers one Serve runs at once when
// ServeLimits leaves Concurrency unset. Sixteen matches the most any one
// caller runs at once (catalog's search consumers); handlers are I/O
// bound -- provider HTTP calls, an index query -- so GOMAXPROCS is not the
// measure.
const DefaultServeConcurrency = 16

// DefaultServeQueue is how many requests one Serve holds waiting for a slot
// when ServeLimits leaves Queue unset. A waiting request's caller deadline
// still bounds its wait, so the queue empties of abandoned work by itself.
const DefaultServeQueue = 64

// DefaultPendingBytes is the default byte bound of a core subscription's
// client-side buffer (ServeLimits.MaxPendingBytes, CoreLimits): the
// server's own 8 MiB max_payload, so one request of the largest size the
// server accepts still fits (ADR-0020 §2). nats.go's own default is 64 MiB
// per subscription.
const DefaultPendingBytes = 8 << 20

// ServePendingPerSlot is how many requests per handler slot one Serve's
// subscription buffers client-side by default (ServeLimits.MaxPending): 4 x
// Concurrency. The callback never blocks (it admits or refuses at once), so
// the buffer only rides out a burst the callback has not reached yet.
const ServePendingPerSlot = 4

// ServeLimits bounds one Serve call's handlers. A zero field takes its
// default.
type ServeLimits struct {
	// Concurrency is how many handlers run at once.
	Concurrency int

	// Queue is how many further requests wait for a free handler slot
	// before a request is refused with ErrResponderBusy.
	Queue int

	// MaxPending and MaxPendingBytes bound the requests the subscription
	// buffers client-side before its callback takes them (nats.go
	// Subscription.SetPendingLimits; ADR-0020 §2). Past them nats.go drops
	// the request as a slow consumer, which is counted and alerted on
	// (clustarr_nats_async_errors_total{kind="slow_consumer"},
	// clustarr_nats_dropped_messages). Defaults: ServePendingPerSlot x
	// Concurrency messages and DefaultPendingBytes.
	MaxPending      int
	MaxPendingBytes int
}

// WithDefaults fills every unset field with its default.
func (l ServeLimits) WithDefaults() ServeLimits {
	if l.Concurrency <= 0 {
		l.Concurrency = DefaultServeConcurrency
	}
	if l.Queue <= 0 {
		l.Queue = DefaultServeQueue
	}
	if l.MaxPending <= 0 {
		l.MaxPending = ServePendingPerSlot * l.Concurrency
	}
	if l.MaxPendingBytes <= 0 {
		l.MaxPendingBytes = DefaultPendingBytes
	}
	return l
}

// DefaultCorePending is the default message bound of a SubscribeCore
// subscription's client-side buffer (CoreLimits.MaxPending).
const DefaultCorePending = 1024

// CoreLimits bounds one SubscribeCore subscription's client-side buffer
// (ADR-0020 §2). A zero field takes its default: DefaultCorePending
// messages, DefaultPendingBytes bytes. Past them nats.go drops messages as
// a slow consumer, counted and alerted on like a responder's.
type CoreLimits struct {
	MaxPending      int
	MaxPendingBytes int
}

// WithDefaults fills every unset field with its default.
func (l CoreLimits) WithDefaults() CoreLimits {
	if l.MaxPending <= 0 {
		l.MaxPending = DefaultCorePending
	}
	if l.MaxPendingBytes <= 0 {
		l.MaxPendingBytes = DefaultPendingBytes
	}
	return l
}

// ServeGate admits requests to one Serve call's handlers under its limits.
// Every bus builds one per Serve call.
type ServeGate struct {
	admitted chan struct{}
	running  chan struct{}
}

// NewServeGate builds a gate for l, defaults applied.
func NewServeGate(l ServeLimits) *ServeGate {
	l = l.WithDefaults()
	return &ServeGate{
		admitted: make(chan struct{}, l.Concurrency+l.Queue),
		running:  make(chan struct{}, l.Concurrency),
	}
}

// Admit takes a place for one request, running or waiting, without
// blocking. It reports false when every place is taken, and the request
// must then be refused with ErrResponderBusy. Otherwise the caller must call
// leave once the request is done, whether or not it ran.
func (g *ServeGate) Admit() (leave func(), ok bool) {
	select {
	case g.admitted <- struct{}{}:
		return func() { <-g.admitted }, true
	default:
		return nil, false
	}
}

// Acquire waits for a handler slot until ctx is done. On success the caller
// must call release once its handler has returned. A ctx already done when
// a slot frees is an error, not a slot: a request whose caller has given up
// is never run.
func (g *ServeGate) Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.running <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-g.running
		return nil, err
	}
	return func() { <-g.running }, nil
}

// FormatTimeout renders the time left before ctx's deadline for
// HeaderTimeout. It reports false for a ctx with no deadline. A deadline
// already past renders as "0", which a responder reads as expired.
func FormatTimeout(ctx context.Context) (string, bool) {
	dl, ok := ctx.Deadline()
	if !ok {
		return "", false
	}
	return strconv.FormatInt(max(time.Until(dl).Milliseconds(), 0), 10), true
}

// ParseTimeout reads a HeaderTimeout value. It reports false for an absent
// or malformed value, which a responder treats as "no caller deadline". A
// zero or negative duration means the caller's time had run out.
func ParseTimeout(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

// HandlerTimeout is how long a responder lets one handler run: fallback
// (the bus's own request timeout; zero or less means none) capped by the
// caller's remaining time when the request carried one. ok is false when
// neither bounds it. expired is true when the caller's time had already run
// out, and the request must then be dropped without running.
func HandlerTimeout(fallback, caller time.Duration, hasCaller bool) (d time.Duration, ok, expired bool) {
	if hasCaller {
		if caller <= 0 {
			return 0, true, true
		}
		if fallback > 0 && fallback < caller {
			return fallback, true, false
		}
		return caller, true, false
	}
	if fallback > 0 {
		return fallback, true, false
	}
	return 0, false, false
}

// ResponderError is a responder's failure as Request reports it. Only the
// handler's message and a code cross the wire, so that is all it holds on
// every bus: errors.Is finds ErrResponderFailed or ErrResponderBusy through
// it, never the handler's own sentinels, on membus exactly as on natsbus. A
// caller that must tell one handler failure from another reads a field of
// the reply body (schema.MetadataResponse.Error is the pattern), not the
// error chain.
type ResponderError struct {
	// Code is ServiceErrorFailed or ServiceErrorBusy; any other code a
	// foreign responder sends reads as failed.
	Code int

	// Message is the responder's error text.
	Message string
}

func (e *ResponderError) Error() string { return e.Message }

// Unwrap returns the sentinel for e's code.
func (e *ResponderError) Unwrap() error {
	if e.Code == ServiceErrorBusy {
		return ErrResponderBusy
	}
	return ErrResponderFailed
}

// RequestErrorClass is what a Request's error says about the verb's
// responder (ADR-0020 §4), for its caller to switch on.
type RequestErrorClass string

// The request error classes.
const (
	// RequestOK is a nil error.
	RequestOK RequestErrorClass = ""
	// RequestNoResponders: nothing serves the verb (ErrNoResponders). It
	// fails fast and no handler ran. In the manager the bus's request
	// observer feeds it to app/dispatch as the serving domain's NoAgent.
	RequestNoResponders RequestErrorClass = "noResponders"
	// RequestBusy: refused at once, before any handler ran -- a responder
	// with no slot and no queue place (ErrResponderBusy), or a request the
	// client could not even buffer while reconnecting (ErrTransient).
	RequestBusy RequestErrorClass = "busy"
	// RequestTimeout: the caller's deadline passed. The responder may be
	// slow or busy, or its answer was lost: a handler may have run.
	RequestTimeout RequestErrorClass = "timeout"
	// RequestOther: anything else, a handler's failure included
	// (ErrResponderFailed).
	RequestOther RequestErrorClass = "other"
)

// ClassifyRequestError classifies a Requester.Request error (ADR-0020 §4).
func ClassifyRequestError(err error) RequestErrorClass {
	switch {
	case err == nil:
		return RequestOK
	case errors.Is(err, ErrNoResponders):
		return RequestNoResponders
	case errors.Is(err, ErrResponderBusy), errors.Is(err, ErrTransient):
		return RequestBusy
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return RequestTimeout
	default:
		return RequestOther
	}
}

// NotRun reports that no handler ran: nothing served the verb, or it was
// refused at once. Even a verb that is not idempotent may be sent again.
func (c RequestErrorClass) NotRun() bool {
	return c == RequestNoResponders || c == RequestBusy
}

// Retryable reports that an idempotent verb should be asked again on its
// caller's own backoff: no responder, busy, or timed out. A verb that is not
// idempotent retries only what NotRun admits, and after a timeout re-reads
// its state first (ADR-0020 §4).
func (c RequestErrorClass) Retryable() bool {
	return c.NotRun() || c == RequestTimeout
}

// Reason is a condition reason for a request that failed with class c:
// NoAgent (no responder), AgentBusy, RPCTimeout, else RPCError -- the
// reason every RPC failure read as before ADR-0020 §4.
func (c RequestErrorClass) Reason() string {
	switch c {
	case RequestNoResponders:
		return "NoAgent"
	case RequestBusy:
		return "AgentBusy"
	case RequestTimeout:
		return "RPCTimeout"
	default:
		return "RPCError"
	}
}

// DescribeRequestError wraps a failed request's error with what its class
// means for subject, for a log line, a returned error or a status message:
// "no responder serves <subject>", "<subject> is busy", "<subject> timed
// out". nil stays nil; errors.Is still finds every sentinel through it.
func DescribeRequestError(subject string, err error) error {
	switch ClassifyRequestError(err) {
	case RequestOK:
		return nil
	case RequestNoResponders:
		return fmt.Errorf("no responder serves %s (no agent of its domain is up): %w", subject, err)
	case RequestBusy:
		return fmt.Errorf("%s is busy; asked again on the caller's backoff: %w", subject, err)
	case RequestTimeout:
		return fmt.Errorf("%s timed out (its responder is slow, busy, or its answer was lost): %w", subject, err)
	default:
		return fmt.Errorf("%s: %w", subject, err)
	}
}

// RequestObserver is a bus whose every Request outcome can be observed: the
// manager feeds them to app/dispatch, so a verb that finds no responder
// reads as its serving domain's NoAgent (ADR-0020 §4). natsbus and membus
// implement it; fn is called after every Request with its subject and
// error, nil on success, and must not block. A nil fn stops observing.
type RequestObserver interface {
	ObserveRequests(fn func(subject string, err error))
}

// CallResponder runs h on data, turning a panic into an error so one bad
// request cannot take a service down. stack is non-nil exactly when h
// panicked, for the bus to log; pkg/events itself does not log.
func CallResponder(ctx context.Context,
	h func(ctx context.Context, data []byte) ([]byte, error), data []byte,
) (out, stack []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, stack = nil, debug.Stack()
			if e, ok := r.(error); ok {
				err = fmt.Errorf("handler panicked: %w", e)
			} else {
				err = fmt.Errorf("handler panicked: %v", r)
			}
		}
	}()
	out, err = h(ctx, data)
	return out, nil, err
}

// BusyMessage is the text of the ErrResponderBusy reply a responder under
// limits l sends. It is one line: on NATS it travels in a header.
func BusyMessage(l ServeLimits) string {
	l = l.WithDefaults()
	return fmt.Sprintf("%v: %d handlers running and %d requests waiting",
		ErrResponderBusy, l.Concurrency, l.Queue)
}

// OneLine is the text a responder sends for err: a NATS header value cannot
// carry a line break, so every bus folds them into spaces, membus too, so a
// test on membus sees what a caller over NATS would.
func OneLine(err error) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(err.Error())
}
