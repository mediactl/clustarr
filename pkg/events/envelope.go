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
	"cmp"
	"context"
	"maps"
	"strings"
	"time"
)

// Message headers. Every Clustarr message carries all of them except
// HeaderTrace, which is present only when the producer had an active span.
const (
	// HeaderMsgID is the NATS server-side deduplication key. Its value is
	// derived from the producer's identity, never random: see MsgIDForObject,
	// MsgIDForRelease and MsgIDForSubtitle.
	HeaderMsgID = "Nats-Msg-Id"

	// HeaderID carries the envelope's ID beside Nats-Msg-Id. When a
	// JetStream schedule fires, the server strips Nats-Msg-Id from the copy
	// (nats-server scheduler.go:214-231), so without this a scheduled task
	// reached its handler with no ID and its dead-letter copies shared one
	// Msg-Id (split spec §9.3 as amended 2026-10-07, M2).
	HeaderID = "Clustarr-Id"

	// HeaderType is the coarse message type, e.g. "catalog.search".
	HeaderType = "Clustarr-Type"

	// HeaderSchema names the payload struct and its version, e.g.
	// "catalog.SearchTask.v1". Payloads are versioned by this header and
	// never by the subject.
	HeaderSchema = "Clustarr-Schema"

	// HeaderSource is "<service>@<version>", e.g. "catalogarr@0.3.1+abc1234".
	HeaderSource = "Clustarr-Source"

	// HeaderKey is the "<namespace>/<name>" of the custom resource the
	// message belongs to. The DLQ projector uses it to find the CR to mark.
	HeaderKey = "Clustarr-Key"

	// HeaderTime is the producer timestamp in RFC 3339 format.
	HeaderTime = "Clustarr-Time"

	// HeaderTrace is a W3C traceparent, propagating the producer's span.
	HeaderTrace = "Clustarr-Trace"

	// HeaderContentType is always ContentTypeJSON.
	HeaderContentType = "Content-Type"
)

// Headers added by the dead-letter path on top of the original message
// headers when a message is copied to CLUSTARR_DLQ.
const (
	HeaderDLQReason   = "Clustarr-DLQ-Reason"
	HeaderDLQAttempts = "Clustarr-DLQ-Attempts"
	HeaderDLQConsumer = "Clustarr-DLQ-Consumer"
	HeaderDLQSubject  = "Clustarr-DLQ-Subject"

	// HeaderDLQMsgID preserves the original Nats-Msg-Id. The copy needs an
	// ID of its own so it is not deduplicated against the original, and a
	// replay restores this one.
	HeaderDLQMsgID = "Clustarr-DLQ-Msg-Id"
)

// ContentTypeJSON is the only payload encoding Clustarr uses on the bus.
const ContentTypeJSON = "application/json"

// Envelope is the transport-independent form of a Clustarr message. Headers
// holds any extra headers beyond the named fields; the named fields win when
// both are set.
type Envelope struct {
	// ID is the deduplication key sent as HeaderMsgID, and again as
	// HeaderID, which a fired schedule keeps.
	ID string

	// Type is the coarse message type sent as HeaderType.
	Type string

	// Schema names the payload struct and version, sent as HeaderSchema.
	Schema string

	// Source is "<service>@<version>", sent as HeaderSource.
	Source string

	// Key is the "<namespace>/<name>" of the owning custom resource.
	Key string

	// Time is the producer timestamp, sent as HeaderTime.
	Time time.Time

	// Trace is the W3C traceparent, sent as HeaderTrace.
	Trace string

	// Headers carries additional headers verbatim.
	Headers map[string]string

	// Data is the JSON-encoded payload.
	Data []byte
}

// Clone returns a deep copy of e. It is safe to mutate the result without
// affecting the original.
func (e *Envelope) Clone() *Envelope {
	if e == nil {
		return nil
	}
	out := *e
	if e.Headers != nil {
		out.Headers = maps.Clone(e.Headers)
	}
	if e.Data != nil {
		out.Data = append([]byte(nil), e.Data...)
	}
	return &out
}

// Header returns the value of the named header, preferring the named
// Envelope fields over the Headers map.
func (e *Envelope) Header(name string) string {
	switch name {
	case HeaderMsgID, HeaderID:
		return e.ID
	case HeaderType:
		return e.Type
	case HeaderSchema:
		return e.Schema
	case HeaderSource:
		return e.Source
	case HeaderKey:
		return e.Key
	case HeaderTrace:
		return e.Trace
	case HeaderContentType:
		return ContentTypeJSON
	case HeaderTime:
		if e.Time.IsZero() {
			return ""
		}
		return e.Time.UTC().Format(time.RFC3339Nano)
	}
	return e.Headers[name]
}

// ToHeaders flattens the envelope into the wire header set.
func (e *Envelope) ToHeaders() map[string]string {
	h := make(map[string]string, len(e.Headers)+8)
	maps.Copy(h, e.Headers)
	for _, name := range []string{
		HeaderMsgID, HeaderID, HeaderType, HeaderSchema, HeaderSource,
		HeaderKey, HeaderTime, HeaderTrace, HeaderContentType,
	} {
		if v := e.Header(name); v != "" {
			h[name] = v
		}
	}
	return h
}

// EnvelopeFromHeaders rebuilds an Envelope from wire headers and a payload.
// Headers that map onto a named field are lifted out of the Headers map. The
// ID is Nats-Msg-Id, else Clustarr-Id (a fired schedule has only the latter),
// and a transport header the broker added or read in transit is dropped.
func EnvelopeFromHeaders(h map[string]string, data []byte) *Envelope {
	e := &Envelope{Data: data}
	rest := make(map[string]string, len(h))
	var msgID, clustarrID string
	for k, v := range h {
		switch {
		case k == HeaderMsgID:
			msgID = v
		case k == HeaderID:
			clustarrID = v
		case k == HeaderType:
			e.Type = v
		case k == HeaderSchema:
			e.Schema = v
		case k == HeaderSource:
			e.Source = v
		case k == HeaderKey:
			e.Key = v
		case k == HeaderTrace:
			e.Trace = v
		case k == HeaderContentType:
			// Implied; not round-tripped into Headers.
		case k == HeaderTime:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				e.Time = t
			}
		case transportHeader(k):
			// How the message travelled, not what it is: never copied on.
		default:
			rest[k] = v
		}
	}
	e.ID = cmp.Or(msgID, clustarrID)
	if len(rest) > 0 {
		e.Headers = rest
	}
	return e
}

// transportHeader reports a header the broker adds or reads in transit: a
// schedule's (Nats-Schedule*, Nats-Scheduler), an expectation's
// (Nats-Expected-*), a per-message TTL or a rollup. A copy published with
// Nats-Schedule-Next onto a stream that allows no schedules is refused with
// 10188, so none may ride into an envelope (M2).
func transportHeader(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "nats-schedule") ||
		strings.HasPrefix(n, "nats-expected-") ||
		n == "nats-ttl" || n == "nats-rollup"
}

// WorkQueue is the acknowledgement contract a handler holds over a single
// in-flight message. Exactly one of Ack, Nak or Term must be called for every
// delivery; InProgress may be called any number of times before that to
// extend the acknowledgement deadline.
//
// The bus calls these on the handler's behalf based on the error the handler
// returns, so handlers normally use Retry and Discard instead. They are
// exported for handlers that need to settle a message early, for example a
// long import that acknowledges before its final bookkeeping.
type WorkQueue interface {
	// Ack marks the delivery successful. On a WorkQueue-retention stream the
	// message is removed.
	Ack(ctx context.Context) error

	// Nak schedules a redelivery after delay. A zero delay redelivers
	// immediately.
	Nak(ctx context.Context, delay time.Duration) error

	// Term stops redelivery regardless of MaxDeliver and records reason.
	Term(ctx context.Context, reason string) error

	// InProgress resets the acknowledgement deadline for this delivery. The
	// spec calls this a heartbeat.
	InProgress(ctx context.Context) error
}

// Message is a single delivery of an Envelope to a subscriber.
type Message interface {
	WorkQueue

	// Envelope returns the decoded message. The returned pointer is owned by
	// the caller for the duration of the handler.
	Envelope() *Envelope

	// Subject is the subject the message was published on.
	Subject() string

	// Attempt is the 1-based delivery count, including this delivery.
	Attempt() uint64
}

// Handler processes one message. Returning nil acknowledges it; returning an
// error built by Retry schedules a redelivery; returning an error built by
// Discard dead-letters it immediately. Any other error is treated as a retry
// using the subscription's backoff schedule.
type Handler func(ctx context.Context, m Message) error
