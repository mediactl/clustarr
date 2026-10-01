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
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// The request/reply rules every bus shares. They live here, not in each
// bus, so natsbus and membus cannot drift apart on them.
//
// A responder runs up to ServeLimits.Concurrency handlers at once per Serve
// call, each on its own goroutine. Before 2026-10-01 natsbus ran the handler
// inside the nats.go subscription callback, which nats.go calls serially, so
// every RPC verb was answered one request at a time per replica: indexarr's
// one replica searched one query at a time while sixteen search consumers
// waited on it, and a request queued behind a slow one started after its
// caller had given up, so the caller retried and the queue only grew.
//
// A request that finds every slot busy waits for one, up to
// ServeLimits.Queue requests per Serve; past that it is answered at once
// with ErrResponderBusy, a header-only reply on NATS, so the caller fails
// fast instead of waiting out its deadline for nothing. A queue rather than
// an immediate refusal, because callers' bursts are routinely a little
// above the concurrency (several catalogarr replicas' search consumers
// against one indexarr), and a short wait inside the caller's own deadline
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
// caller runs at once (catalogarr's search consumers); handlers are I/O
// bound -- provider HTTP calls, an index query -- so GOMAXPROCS is not the
// measure.
const DefaultServeConcurrency = 16

// DefaultServeQueue is how many requests one Serve holds waiting for a slot
// when ServeLimits leaves Queue unset. A waiting request's caller deadline
// still bounds its wait, so the queue empties of abandoned work by itself.
const DefaultServeQueue = 64

// ServeLimits bounds one Serve call's handlers. A zero field takes its
// default.
type ServeLimits struct {
	// Concurrency is how many handlers run at once.
	Concurrency int

	// Queue is how many further requests wait for a free handler slot
	// before a request is refused with ErrResponderBusy.
	Queue int
}

// WithDefaults fills every unset field with its default.
func (l ServeLimits) WithDefaults() ServeLimits {
	if l.Concurrency <= 0 {
		l.Concurrency = DefaultServeConcurrency
	}
	if l.Queue <= 0 {
		l.Queue = DefaultServeQueue
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
