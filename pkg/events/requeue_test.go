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

package events_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// A requeue is never a dead letter, whatever the attempt: it re-enqueues a
// fresh copy, which has spent no delivery, so the final attempt is no
// different from the first.
func TestSettleRequeueSpendsNoAttempt(t *testing.T) {
	sub := events.Subscription{MaxDeliver: 3, Backoff: []time.Duration{time.Minute, 10 * time.Minute}, RequeueOnStop: true}
	for attempt := uint64(1); attempt <= 5; attempt++ {
		s := events.Settle(events.Requeue("stopping", context.Canceled), attempt, sub)
		assert.Equal(t, events.SettleRequeue, s.Action, "attempt %d", attempt)
		assert.Equal(t, "stopping", s.Reason, "attempt %d", attempt)
	}
	// A Discard still wins: a poison message is not handed back.
	s := events.Settle(events.Requeue("stopping", events.Discard("bad", nil)), 1, sub)
	assert.Equal(t, events.SettleTerm, s.Action)
}

// A handler a stopping RequeueOnStop subscription cancelled is requeued
// whatever error it returns -- a decode cut off, a KV write refused on the
// cancelled context -- unless it finished (nil) or refused the message
// (Discard). On any other subscription, or for any other cause, its error
// stands.
func TestRequeueIfStopping(t *testing.T) {
	stopping, cancel := context.WithCancelCause(context.Background())
	cancel(events.ErrStopping)
	budget, cancelBudget := context.WithCancelCause(context.Background())
	cancelBudget(events.ErrHandlerBudget)
	on := events.Subscription{RequeueOnStop: true}
	boom := errors.New("decode: cancelled")

	var rq *events.RequeueError
	require.ErrorAs(t, events.RequeueIfStopping(stopping, boom, on), &rq)
	assert.ErrorIs(t, rq, boom, "the cause is kept")
	assert.NoError(t, events.RequeueIfStopping(stopping, nil, on), "a handler that finished is acked")
	var d *events.DiscardError
	assert.ErrorAs(t, events.RequeueIfStopping(stopping, events.Discard("bad", nil), on), &d)
	assert.Same(t, boom, events.RequeueIfStopping(stopping, boom, events.Subscription{}), "not a requeue-on-stop durable")
	assert.Same(t, boom, events.RequeueIfStopping(budget, boom, on), "a spent budget is not a stop")
	assert.Same(t, boom, events.RequeueIfStopping(context.Background(), boom, on))
}

// The copy's Msg-Id names the delivery it replaces, so the stream's
// deduplication window keeps it beside its original's, and a second
// requeue replaces the first's suffix rather than growing the ID.
func TestRequeueMsgID(t *testing.T) {
	first := events.RequeueMsgID("segments-analyze-media/show-s01-0123", 41)
	assert.Equal(t, "segments-analyze-media/show-s01-0123/requeue-41", first)
	assert.Equal(t, "segments-analyze-media/show-s01-0123/requeue-97", events.RequeueMsgID(first, 97))
	assert.Equal(t, "requeue-7", events.RequeueMsgID("", 7))
}

// markers-analyze hands its deliveries back on a stop: a season cut off by
// a rollout is requeued, never spends one of its three deliveries, and the
// pod's grace is the requeue drain, not the 30-minute AckWait.
func TestMarkersAnalyzeRequeuesOnStop(t *testing.T) {
	c, ok := events.Default().Consumer(events.ConsumerMarkersAnalyze)
	require.True(t, ok)
	assert.True(t, c.RequeueOnStop)
	assert.True(t, c.Subscription().RequeueOnStop, "the subscription carries it")
	assert.Equal(t, events.RequeueDrain, c.StopBudget())
	assert.Less(t, c.StopBudget(), time.Minute, "a markers pod stops inside a minute")
	scan, ok := events.Default().Consumer(events.ConsumerImportScan)
	require.True(t, ok)
	assert.False(t, scan.RequeueOnStop)
	assert.Equal(t, scan.AckWait, scan.StopBudget(), "every other durable still drains for its AckWait")
	assert.True(t, events.SubscriptionSpec(c.Subscription()).RequeueOnStop, "SubscriptionSpec inverts it")
}

// A copy re-enqueued on a Limits stream would reach every durable bound to
// it, not only the one that gave it back: RequeueOnStop is refused off a
// WorkQueue stream.
func TestValidateRefusesRequeueOnStopOffAWorkQueue(t *testing.T) {
	c := events.ConsumerSpec{
		Name: "ct-requeue", Stream: events.StreamEvents, Filters: []string{events.FilterAllEvents},
		AckWait: time.Minute, MaxDeliver: 3, MaxAckPending: 1, Slots: 1, RequeueOnStop: true,
	}
	err := withConsumer(c).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ct-requeue.RequeueOnStop")
	c.Stream, c.Filters = events.StreamWorkIndex, []string{events.FilterIndexRSS}
	assert.NoError(t, withConsumer(c).Validate())
}
