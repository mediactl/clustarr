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
	"strconv"
	"strings"
	"time"
)

// ErrStopping is the cause a RequeueOnStop subscription's running handlers
// are cancelled with when it stops (Subscription.RequeueOnStop): the process
// is going away, and what the handlers hold goes back to the stream.
var ErrStopping = errors.New("events: the subscription is stopping; its deliveries are requeued")

// RequeueDrain is a RequeueOnStop durable's StopBudget: how long a stopping
// process waits for its cancelled handlers to return and settle -- an
// in-process call's own grace once its context ends (ffruntime.Grace, 5 s),
// then the copy's publish and the delivery's ack, a server round trip each.
// The Deployment's terminationGracePeriodSeconds covers it plus 25 s
// (test/guards.TestGracePeriodsCoverAckWait).
const RequeueDrain = 15 * time.Second

// RequeueError hands a delivery back as a fresh message: the bus re-enqueues
// a copy -- the same subject, envelope and payload, under a Msg-Id of its own
// (RequeueMsgID) -- and then acknowledges the delivery. The copy has spent no
// delivery, so the requeue never counts toward MaxDeliver and is never a
// dead letter, on the final attempt as on the first. It is for work cut off
// through no fault of the message, a stopping process (RequeueIfStopping),
// never for a failure, which must spend its attempts so a poison message is
// still dead-lettered. The copy joins the back of the queue.
//
// Only a subscription with RequeueOnStop requeues -- Validate holds those to
// a WorkQueue stream, where the copy reaches only the durable that gave it
// back -- and on any other the bus naks it at once, spending a delivery. If
// the copy cannot be stored, the bus naks the delivery instead.
type RequeueError struct {
	Reason string
	Err    error
}

func (e *RequeueError) Error() string {
	if e.Err == nil {
		return "requeue: " + e.Reason
	}
	return fmt.Sprintf("requeue: %s: %v", e.Reason, e.Err)
}

// Unwrap returns the wrapped cause.
func (e *RequeueError) Unwrap() error { return e.Err }

// Requeue builds an error that hands the delivery back as a fresh message
// (RequeueError).
func Requeue(reason string, err error) error {
	return &RequeueError{Reason: reason, Err: err}
}

// RequeueIfStopping is the error a handler's result settles as on s: err,
// unless s requeues on a stop, the bus cancelled the handler for one (ctx's
// cause is ErrStopping) and err is neither nil -- the handler finished -- nor
// a Discard -- it refused the message. Then it is a requeue, whatever err
// says: a decode cut off, a write refused on the cancelled context. Both
// buses apply it before Settle.
func RequeueIfStopping(ctx context.Context, err error, s Subscription) error {
	if err == nil || !s.RequeueOnStop || !errors.Is(context.Cause(ctx), ErrStopping) {
		return err
	}
	var d *DiscardError
	var r *RequeueError
	if errors.As(err, &d) || errors.As(err, &r) {
		return err
	}
	return Requeue("the subscription stopped", err)
}

const requeueSuffix = "/requeue-"

// RequeueMsgID is the Msg-Id of the copy that requeues the stored message
// seq whose Msg-Id is id: id with "/requeue-<seq>" in place of any earlier
// requeue's suffix. The original's own Msg-Id may still sit in the stream's
// deduplication window, which would drop a copy under it; seq makes the copy
// unique, and the same delivery requeued twice (a lapsed handler and its
// redelivery) one copy.
func RequeueMsgID(id string, seq uint64) string {
	if i := strings.LastIndex(id, requeueSuffix); i >= 0 {
		if _, err := strconv.ParseUint(id[i+len(requeueSuffix):], 10, 64); err == nil {
			id = id[:i]
		}
	}
	n := strconv.FormatUint(seq, 10)
	if id == "" {
		return strings.TrimPrefix(requeueSuffix, "/") + n
	}
	return id + requeueSuffix + n
}
