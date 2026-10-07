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

package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxDeliveriesAdvisoryType is the advisory's schema type, nats-server's
// JSConsumerDeliveryExceededAdvisoryType, restated because importing the
// server package to name one string would link the whole broker into the
// binary; the natsbus tests hold it, and events.SubjectMaxDeliveriesAdvisoryPrefix,
// equal to the server's.
const maxDeliveriesAdvisoryType = "io.nats.jetstream.advisory.v1.max_deliver"

// deadLetterTimeout bounds reading, copying and deleting one lapsed message.
// events.DeadLetterWatcherAckWait covers events.DeadLetterWatcherMaxAckPending
// of these, handled one at a time, twice over.
const deadLetterTimeout = 10 * time.Second

// watchRetry is the delay before an advisory whose copy failed is handled
// again, by attempt; attempts past the end reuse the last entry. A watcher
// retries for as long as it takes (events.DeadLetterWatcherSpec's MaxDeliver
// -1), rather than giving up: the message an advisory names is otherwise
// never dead-lettered, and on a work-queue stream never removed either.
var watchRetry = []time.Duration{
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
}

// maxDeliveriesAdvisory is the part of nats-server's
// JSConsumerDeliveryExceededAdvisory the watcher reads.
type maxDeliveriesAdvisory struct {
	Type       string `json:"type"`
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries uint64 `json:"deliveries"`
}

var _ events.DeadLetterWatcher = (*Bus)(nil)

// runWatcher dead-letters what sub's watcher on events.StreamAdvisories
// reports, one advisory at a time, until ctx ends, completing the DLQ path
// for the one case events.Settle cannot see: a handler that hangs through its
// final delivery never returns, so nothing in process ever settles or copies
// the message.
//
// The watcher is a topology object (events.DeadLetterWatcherSpec): the
// manager's EnsureTopology creates it, and runWatcher only binds it, waiting
// while it is missing and binding it again if it disappears, exactly as the
// pull loop binds a durable. It reads the advisories from
// events.StreamAdvisories, which captures them as JetStream publishes them,
// through a durable filtered to sub's own advisory subject. So an advisory
// fired while no replica of sub's consumer is subscribed -- JetStream fires
// it when it next tries to deliver, and a pull request a stopping replica
// left behind is enough for that -- waits for a watcher instead of being
// lost, and one whose copy fails is naked onto watchRetry rather than
// dropped.
//
// Every process consuming sub's durable runs it, and the manager runs it too
// as a backstop (WatchDeadLetters), for a domain at zero replicas. They all
// share the one watcher durable, so each advisory is handled once, and the
// copy's Msg-Id deduplicates an advisory handled twice anyway.
func (b *Bus) runWatcher(ctx context.Context, sub events.Subscription) {
	name := events.DeadLetterWatcherName(sub.Stream, sub.Durable)
	base := context.WithoutCancel(ctx)
	var cons jetstream.Consumer
	for ctx.Err() == nil {
		if cons == nil {
			c, err := b.bindConsumer(ctx, events.StreamAdvisories, name)
			if err != nil {
				return
			}
			cons = c
		}
		err := b.watchOnce(ctx, base, cons, sub)
		switch {
		case err == nil, ctx.Err() != nil, benignFetchError(err):
		case consumerGone(err):
			cons = nil
		default:
			logging.FromContext(ctx).Warn("bus: dead-letter watcher fetch failed; retrying",
				"watcher", name, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(fetchRetry):
			}
		}
	}
}

// watchOnce fetches up to events.DeadLetterWatcherMaxAckPending advisories
// and handles each: acknowledged once dead-lettered, naked onto watchRetry
// when the copy failed. The copies run on base, so stopping the watcher
// does not cut one short.
func (b *Bus) watchOnce(ctx, base context.Context, cons jetstream.Consumer, sub events.Subscription) error {
	fctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	batch, err := cons.Fetch(events.DeadLetterWatcherMaxAckPending,
		jetstream.FetchContext(fctx), jetstream.FetchHeartbeat(fetchHeartbeat))
	if err != nil {
		return err
	}
	for m := range batch.Messages() {
		if b.deadLetterLapsed(base, sub, m.Data()) {
			_ = m.Ack()
			continue
		}
		attempt := uint64(1)
		if md, err := m.Metadata(); err == nil && md.NumDelivered > 0 {
			attempt = md.NumDelivered
		}
		_ = m.NakWithDelay(watchRetry[min(attempt, uint64(len(watchRetry)))-1])
	}
	return batch.Error()
}

// WatchDeadLetters implements events.DeadLetterWatcher. It binds sub's
// watcher, which EnsureTopology created, and never creates it; Close stops it.
func (b *Bus) WatchDeadLetters(ctx context.Context, sub events.Subscription) (func(), error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, events.ErrClosed
	}
	wctx, cancel := context.WithCancel(ctx)
	unhook := context.AfterFunc(b.serveCtx, cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.runWatcher(wctx, sub)
	}()
	return func() {
		unhook()
		cancel()
		<-done
	}, nil
}

// deadLetterLapsed handles one MAX_DELIVERIES advisory for sub. It reports
// whether the advisory is finished with: false means the copy failed and the
// advisory should be handled again.
func (b *Bus) deadLetterLapsed(ctx context.Context, sub events.Subscription, data []byte) bool {
	log := logging.FromContext(ctx)
	var adv maxDeliveriesAdvisory
	if err := json.Unmarshal(data, &adv); err != nil {
		log.Warn("bus: ignoring an undecodable MAX_DELIVERIES advisory",
			"durable", sub.Durable, "error", err)
		return true
	}
	if adv.Type != maxDeliveriesAdvisoryType || adv.Stream != sub.Stream ||
		adv.Consumer != sub.Durable || adv.StreamSeq == 0 {
		log.Warn("bus: ignoring a MAX_DELIVERIES advisory for another consumer",
			"durable", sub.Durable, "type", adv.Type, "stream", adv.Stream,
			"consumer", adv.Consumer, "stream_seq", adv.StreamSeq)
		return true
	}
	if adv.Deliveries == 0 && sub.MaxDeliver > 0 {
		adv.Deliveries = uint64(sub.MaxDeliver)
	}

	ctx, cancel := context.WithTimeout(ctx, deadLetterTimeout)
	defer cancel()
	msgID, dup, err := b.copyLapsed(ctx, adv)
	switch {
	case errors.Is(err, jetstream.ErrMsgNotFound):
		// A re-sent advisory whose message an earlier copy already
		// dead-lettered and deleted, or one the stream's limits dropped.
		log.Info("bus: lapsed message is no longer stored; nothing to dead-letter",
			"durable", adv.Consumer, "stream", adv.Stream, "stream_seq", adv.StreamSeq)
		return true
	case err != nil:
		log.Error("bus: final delivery lapsed and was not dead-lettered; will retry",
			"durable", adv.Consumer, "stream", adv.Stream, "stream_seq", adv.StreamSeq,
			"deliveries", adv.Deliveries, "msg_id", msgID, "error", err)
		return false
	default:
		log.Warn("bus: final delivery lapsed unacknowledged; dead-lettered",
			"durable", adv.Consumer, "stream", adv.Stream, "stream_seq", adv.StreamSeq,
			"deliveries", adv.Deliveries, "msg_id", msgID, "duplicate", dup)
		return true
	}
}

// copyLapsed reads the lapsed message back by stream sequence, copies it to
// CLUSTARR_DLQ exactly as the in-process path would, and then, on a
// WorkQueue stream, deletes it: an in-process Term removes a work-queue
// message, and one JetStream has given up on otherwise sits in the stream
// until its limits discard it. A Limits stream keeps it, as it keeps every
// message its consumers have finished with. Nothing is deleted unless the
// copy is stored (or already was), so a failed copy leaves the original in
// place.
func (b *Bus) copyLapsed(ctx context.Context, adv maxDeliveriesAdvisory) (msgID string, dup bool, err error) {
	st, err := b.js.Stream(ctx, adv.Stream)
	if err != nil {
		return "", false, fmt.Errorf("stream %s: %w", adv.Stream, err)
	}
	raw, err := st.GetMsg(ctx, adv.StreamSeq)
	if err != nil {
		return "", false, fmt.Errorf("read %s seq %d: %w", adv.Stream, adv.StreamSeq, err)
	}
	headers := make(map[string]string, len(raw.Header))
	for k := range raw.Header {
		headers[k] = raw.Header.Get(k)
	}
	orig := events.EnvelopeFromHeaders(headers, raw.Data)
	subject, env := events.DeadLetterEnvelope(orig, raw.Subject, adv.Deliveries,
		adv.Consumer, events.AckWaitExhaustedReason(adv.Deliveries))
	rcpt, err := b.Publish(ctx, subject, env)
	if err != nil {
		return orig.ID, false, fmt.Errorf("copy to %s: %w", subject, err)
	}
	if st.CachedInfo().Config.Retention == jetstream.WorkQueuePolicy {
		if err := st.DeleteMsg(ctx, adv.StreamSeq); err != nil {
			return orig.ID, rcpt.Duplicate, fmt.Errorf("dead-lettered, but delete %s seq %d: %w",
				adv.Stream, adv.StreamSeq, err)
		}
	}
	return orig.ID, rcpt.Duplicate, nil
}
