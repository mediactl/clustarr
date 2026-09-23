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

// deadLetterWatchPrefix names each consumer's watcher: the durable consumer
// on events.StreamAdvisories that every replica consuming one durable shares,
// so each advisory is handled by exactly one of them. The copy's Msg-Id is
// the second guard, for an advisory handled twice anyway.
const deadLetterWatchPrefix = "clustarr-dlq-watch-"

// deadLetterTimeout bounds reading, copying and deleting one lapsed message.
const deadLetterTimeout = 10 * time.Second

// The watcher consumer's tuning. An advisory whose copy fails is retried on
// a capped schedule for as long as it takes, rather than given up on: the
// message it names is otherwise never dead-lettered, and on a work-queue
// stream never removed either. AckWait covers the MaxAckPending advisories a
// pull can hold, each allowed deadLetterTimeout, handled one at a time.
const (
	watchMaxAckPending = 4
	watchAckWait       = 2 * watchMaxAckPending * deadLetterTimeout
)

// watchRetry is the delay before an advisory whose copy failed is handled
// again, by attempt; attempts past the end reuse the last entry.
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

// watcherName is the durable name of sub's watcher on events.StreamAdvisories.
// It carries the stream as well as the durable, so two subscriptions that
// reuse a durable name on different streams do not share, and fight over, one
// watcher's filter.
func watcherName(sub events.Subscription) string {
	return deadLetterWatchPrefix + sub.Stream + "-" + sub.Durable
}

// watchMaxDeliveries starts sub's MAX_DELIVERIES watcher and dead-letters
// every message an advisory names, completing the DLQ path for the one case
// events.Settle cannot see: a handler that hangs through its final delivery
// never returns, so nothing in process ever settles or copies the message.
//
// The watcher reads the advisories from events.StreamAdvisories, which
// captures them as JetStream publishes them, through a durable consumer
// filtered to sub's own advisory subject. So an advisory fired while no
// replica of sub's consumer is subscribed -- JetStream fires it when it next
// tries to deliver, and a pull request a stopping replica left behind is
// enough for that -- waits for the next replica to subscribe instead of being
// lost, and one whose copy fails is retried rather than dropped. It was a
// core-NATS queue subscription, which had neither property (gap fixes Z2).
//
// It runs wherever sub is consumed, which is every replica of every service
// with a queue worker (spec §6.1: workers run on every replica), rather than
// in one designated role. The DLQ path is in process by design (spec §5:
// events.Settle, shared by natsbus and membus), so the process that owns a
// consumer owns its dead letters; a watcher confined to catalogarr's history
// role would leave importarr's, indexarr's and captionarr's hung handlers
// dropped whenever that role is not deployed. The shared durable keeps the
// replicas from copying one advisory more than once.
func (b *Bus) watchMaxDeliveries(ctx context.Context, sub events.Subscription) (jetstream.ConsumeContext, error) {
	name := watcherName(sub)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, events.StreamAdvisories, jetstream.ConsumerConfig{
		Name:          name,
		Durable:       name,
		Description:   "Dead-letters the lapsed final deliveries of " + sub.Durable + ".",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: events.MaxDeliveriesAdvisorySubject(sub.Stream, sub.Durable),
		AckWait:       watchAckWait,
		MaxDeliver:    -1,
		MaxAckPending: watchMaxAckPending,
	})
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, fmt.Errorf("natsbus: watch max deliveries of %s: stream %s: %w",
				sub.Durable, events.StreamAdvisories, events.ErrStreamNotFound)
		}
		return nil, fmt.Errorf("natsbus: watch max deliveries of %s: %w", sub.Durable, err)
	}
	base := context.WithoutCancel(ctx)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if b.deadLetterLapsed(base, sub, m.Data()) {
			_ = m.Ack()
			return
		}
		attempt := uint64(1)
		if md, err := m.Metadata(); err == nil && md.NumDelivered > 0 {
			attempt = md.NumDelivered
		}
		_ = m.NakWithDelay(watchRetry[min(attempt, uint64(len(watchRetry)))-1])
	}, jetstream.PullMaxMessages(watchMaxAckPending))
	if err != nil {
		return nil, fmt.Errorf("natsbus: watch max deliveries of %s: %w", sub.Durable, err)
	}
	return cc, nil
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
