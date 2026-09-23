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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned by every Bus implementation.
var (
	// ErrQueueFull is returned by Publish when a DiscardNew work stream is at
	// its byte limit. Producers should retry later rather than drop the task.
	ErrQueueFull = errors.New("events: work queue full")

	// ErrNoResponders is returned by Request when nothing is serving the
	// subject.
	ErrNoResponders = errors.New("events: no responders")

	// ErrStreamNotFound is returned when a subscription names a stream that
	// EnsureTopology has not created.
	ErrStreamNotFound = errors.New("events: stream not found")

	// ErrBucketNotFound is returned by KV operations on an unknown bucket.
	ErrBucketNotFound = errors.New("events: bucket not found")

	// ErrKeyNotFound is returned by KV.Get and KV.Update for a missing key.
	ErrKeyNotFound = errors.New("events: key not found")

	// ErrKeyExists is returned by KV.Create when the key is already present.
	// It is the double-grab guard used by the clustarr-leases bucket.
	ErrKeyExists = errors.New("events: key exists")

	// ErrRevisionMismatch is returned by KV.Update and KV.DeleteRevision when
	// the supplied revision is not the key's current revision.
	ErrRevisionMismatch = errors.New("events: key revision mismatch")

	// ErrClosed is returned by every method once Close has been called.
	ErrClosed = errors.New("events: bus closed")

	// ErrRetentionImmutable is returned by Ensure when an existing stream's
	// retention policy differs from the topology. An operator must migrate
	// the stream by hand; silently recreating it would drop queued work.
	ErrRetentionImmutable = errors.New("events: stream retention is immutable")
)

// RetryError asks the bus to redeliver the message after After. A zero After
// means "use the subscription's backoff schedule for this attempt".
type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("retry after %s", e.After)
	}
	return fmt.Sprintf("retry after %s: %v", e.After, e.Err)
}

// Unwrap returns the wrapped cause.
func (e *RetryError) Unwrap() error { return e.Err }

// Retry builds an error that makes the bus redeliver the message after the
// given delay instead of consulting the subscription backoff schedule.
func Retry(after time.Duration, err error) error {
	return &RetryError{After: after, Err: err}
}

// DiscardError tells the bus to dead-letter the message immediately: the
// payload is copied to clustarr.dlq.<service>.<task>.<id> with Reason
// attached and the delivery is terminated, bypassing MaxDeliver.
type DiscardError struct {
	Reason string
	Err    error
}

func (e *DiscardError) Error() string {
	if e.Err == nil {
		return "discard: " + e.Reason
	}
	return fmt.Sprintf("discard: %s: %v", e.Reason, e.Err)
}

// Unwrap returns the wrapped cause.
func (e *DiscardError) Unwrap() error { return e.Err }

// Discard builds an error that dead-letters the message immediately.
func Discard(reason string, err error) error {
	return &DiscardError{Reason: reason, Err: err}
}

// SettleAction is how a bus must settle one delivery.
type SettleAction string

// Settlement actions.
const (
	// SettleAck acknowledges the delivery.
	SettleAck SettleAction = "ack"

	// SettleNak schedules a redelivery after Settlement.Delay.
	SettleNak SettleAction = "nak"

	// SettleTerm stops redelivery. The message is copied to the dead-letter
	// stream first, with Settlement.Reason attached.
	SettleTerm SettleAction = "term"
)

// Settlement is the decision a bus takes about one delivery.
type Settlement struct {
	// Action is the settlement to apply.
	Action SettleAction

	// Delay is how long to wait before redelivery. It is meaningful only for
	// SettleNak.
	Delay time.Duration

	// Reason explains a SettleTerm, and becomes the Clustarr-DLQ-Reason
	// header on the dead-lettered copy.
	Reason string
}

// Settle maps a handler result to the settlement the bus must apply. It is
// the one place the redelivery and dead-letter policy lives, so natsbus and
// membus behave identically:
//
//   - nil acknowledges;
//   - a Discard error dead-letters at once, bypassing MaxDeliver;
//   - any other error retries with the explicit Retry delay, or else the
//     subscription's backoff schedule, until attempt reaches MaxDeliver, at
//     which point the message is dead-lettered instead.
//
// A handler that never returns from its final delivery never reaches Settle.
// That message is dead-lettered by the bus itself once the final
// acknowledgement deadline passes, with AckWaitExhaustedReason.
func Settle(err error, attempt uint64, s Subscription) Settlement {
	if err == nil {
		return Settlement{Action: SettleAck}
	}
	var d *DiscardError
	if errors.As(err, &d) {
		return Settlement{Action: SettleTerm, Reason: d.Reason}
	}
	if s.MaxDeliver > 0 && attempt >= uint64(s.MaxDeliver) {
		return Settlement{
			Action: SettleTerm,
			Reason: fmt.Sprintf("max deliveries exceeded (%d): %v", s.MaxDeliver, err),
		}
	}
	delay := Backoff(s.Backoff, attempt)
	var r *RetryError
	if errors.As(err, &r) && r.After > 0 {
		delay = r.After
	}
	return Settlement{Action: SettleNak, Delay: delay}
}

// AckWaitExhaustedReason is the Clustarr-DLQ-Reason of a message whose final
// delivery lapsed unsettled: the handler was still running -- hung, not
// failing -- when the last acknowledgement deadline passed, so the broker gave
// up on the message with no handler error to report. Settle never sees such a
// message, because the handler never returned. natsbus dead-letters it from
// JetStream's MAX_DELIVERIES advisory and membus from its own ack-deadline
// sweep, and both write this reason so the two buses stay indistinguishable.
func AckWaitExhaustedReason(maxDeliver uint64) string {
	return fmt.Sprintf("max deliveries exceeded (%d): acknowledgement timed out", maxDeliver)
}

// DeadLetter builds the envelope and subject for the dead-lettered copy of m,
// preserving the original payload and headers and adding the Clustarr-DLQ-*
// headers the projector reads.
func DeadLetter(m Message, durable, reason string) (subject string, e *Envelope) {
	return DeadLetterEnvelope(m.Envelope(), m.Subject(), m.Attempt(), durable, reason)
}

// DeadLetterEnvelope is DeadLetter for a message the caller holds no delivery
// of -- natsbus's advisory watcher reads the stored message back by stream
// sequence after JetStream has given up on it. orig is the stored envelope,
// subject the subject it was published on and attempts the delivery count.
//
// The copy's ID, and so its Nats-Msg-Id, is derived from durable and the
// original ID alone, so the in-process path and the advisory path dead-letter
// one message under one ID: whichever stores it first wins and the other is a
// duplicate inside CLUSTARR_DLQ's deduplication window.
func DeadLetterEnvelope(orig *Envelope, subject string, attempts uint64,
	durable, reason string,
) (dlqSubject string, e *Envelope) {
	out := orig.Clone()
	if out == nil {
		out = &Envelope{}
	}
	if out.Headers == nil {
		out.Headers = map[string]string{}
	}
	out.Headers[HeaderDLQReason] = reason
	out.Headers[HeaderDLQAttempts] = strconv.FormatUint(attempts, 10)
	out.Headers[HeaderDLQConsumer] = durable
	out.Headers[HeaderDLQSubject] = subject
	if orig != nil && orig.ID != "" {
		out.Headers[HeaderDLQMsgID] = orig.ID
	}
	service, task := dlqServiceTask(subject, durable)
	id := out.ID
	if id == "" {
		id = strconv.FormatUint(attempts, 10)
	}
	out.ID = "dlq:" + durable + ":" + id
	return DLQSubject(service, task, id), out
}

// dlqServiceTask names the <service> and <task> tokens of a dead letter's
// subject, clustarr.dlq.<service>.<task>.<id>.
//
// A work subject, clustarr.work.<service>.<task>..., names both itself. Any
// other subject does not: an event on CLUSTARR_EVENTS
// (clustarr.evt.<group>.<kind>.<action>.<uid>) or a release on
// CLUSTARR_RELEASES names its producer's domain, not the task that failed on
// it, and one event is consumed by several durables -- catalogarr-history and
// catalogarr-redownload both read a Download's failed event. Reading tokens
// 2 and 3 of those gave clustarr.dlq.download.download.<id>, a "service" that
// does not exist, shared by every consumer of the event, so two consumers'
// dead letters of one event collided on one subject and the projector's
// last-sequence lookup could name the other's copy. Those take the tokens
// from the consumer's durable name, <service>-<task>, instead:
// catalogarr-redownload's lands on clustarr.dlq.catalogarr.redownload.<id>.
// Anything unparseable falls back to "unknown".
func dlqServiceTask(subject, durable string) (service, task string) {
	service, task = "unknown", "unknown"
	parts := strings.Split(subject, ".")
	if len(parts) > 2 && parts[0] == SubjectRoot && parts[1] == "work" {
		service = parts[2]
		if len(parts) > 3 {
			task = parts[3]
		}
		return service, task
	}
	if s, t, ok := strings.Cut(durable, "-"); ok && s != "" && t != "" {
		return s, t
	}
	if durable != "" {
		service = durable
	}
	return service, task
}

// Backoff returns the delay to apply before delivery attempt+1, given a
// subscription's backoff schedule. Attempts past the end of the schedule
// reuse its last entry, matching JetStream's own behaviour. An empty schedule
// yields DefaultBackoff.
func Backoff(schedule []time.Duration, attempt uint64) time.Duration {
	if len(schedule) == 0 {
		return DefaultBackoff
	}
	if attempt == 0 {
		attempt = 1
	}
	if int(attempt) > len(schedule) {
		return schedule[len(schedule)-1]
	}
	return schedule[attempt-1]
}

// DefaultBackoff is used when a subscription declares no backoff schedule.
const DefaultBackoff = 30 * time.Second
