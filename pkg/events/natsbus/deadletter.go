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

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The MAX_DELIVERIES advisory. JetStream publishes one on core NATS, at
// maxDeliveriesAdvisoryPrefix.<stream>.<consumer>, when a message's final
// delivery lapses without a settlement: the last acknowledgement deadline
// passed with the handler still running, or the handler naked it. The server
// gives up on the message at that moment whatever the client is doing, and
// the advisory is the only record of it. These are nats-server's
// JSAdvisoryConsumerMaxDeliveryExceedPre and
// JSConsumerDeliveryExceededAdvisoryType, restated because importing the
// server package to name two strings would link the whole broker into the
// binary; the natsbus tests hold them equal to the server's.
const (
	maxDeliveriesAdvisoryPrefix = "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES"
	maxDeliveriesAdvisoryType   = "io.nats.jetstream.advisory.v1.max_deliver"
)

// deadLetterQueuePrefix names the core-NATS queue group every replica's
// watcher for one durable joins, so each advisory is handled by exactly one
// of them. The copy's Msg-Id is the second guard, for an advisory handled
// twice anyway.
const deadLetterQueuePrefix = "clustarr-dlq-watch-"

// deadLetterTimeout bounds reading, copying and deleting one lapsed message.
const deadLetterTimeout = 10 * time.Second

// maxDeliveriesAdvisory is the part of nats-server's
// JSConsumerDeliveryExceededAdvisory the watcher reads.
type maxDeliveriesAdvisory struct {
	Type       string `json:"type"`
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries uint64 `json:"deliveries"`
}

// maxDeliveriesAdvisorySubject is the subject JetStream announces a lapsed
// final delivery of durable on stream under.
func maxDeliveriesAdvisorySubject(stream, durable string) string {
	return maxDeliveriesAdvisoryPrefix + "." + stream + "." + durable
}

// watchMaxDeliveries subscribes to sub's MAX_DELIVERIES advisory and
// dead-letters every message it names, completing the DLQ path for the one
// case events.Settle cannot see: a handler that hangs through its final
// delivery never returns, so nothing in process ever settles or copies the
// message.
//
// It runs wherever sub is consumed, which is every replica of every service
// with a queue worker (spec §6.1: workers run on every replica), rather than
// in one designated role. The DLQ path is in process by design (spec §5:
// events.Settle, shared by natsbus and membus), so the process that owns a
// consumer owns its dead letters; a watcher confined to catalogarr's history
// role would leave importarr's, indexarr's and captionarr's hung handlers
// dropped whenever that role is not deployed. The queue group keeps the
// replicas from copying one advisory more than once.
func (b *Bus) watchMaxDeliveries(ctx context.Context, sub events.Subscription) (*nats.Subscription, error) {
	base := context.WithoutCancel(ctx)
	w, err := b.nc.QueueSubscribe(maxDeliveriesAdvisorySubject(sub.Stream, sub.Durable),
		deadLetterQueuePrefix+sub.Durable,
		func(m *nats.Msg) { b.deadLetterLapsed(base, sub, m.Data) })
	if err != nil {
		return nil, fmt.Errorf("natsbus: watch max deliveries of %s: %w", sub.Durable, err)
	}
	return w, nil
}

// deadLetterLapsed handles one MAX_DELIVERIES advisory for sub.
func (b *Bus) deadLetterLapsed(ctx context.Context, sub events.Subscription, data []byte) {
	log := logging.FromContext(ctx)
	var adv maxDeliveriesAdvisory
	if err := json.Unmarshal(data, &adv); err != nil {
		log.Warn("bus: ignoring an undecodable MAX_DELIVERIES advisory",
			"durable", sub.Durable, "error", err)
		return
	}
	if adv.Type != maxDeliveriesAdvisoryType || adv.Stream != sub.Stream ||
		adv.Consumer != sub.Durable || adv.StreamSeq == 0 {
		log.Warn("bus: ignoring a MAX_DELIVERIES advisory for another consumer",
			"durable", sub.Durable, "type", adv.Type, "stream", adv.Stream,
			"consumer", adv.Consumer, "stream_seq", adv.StreamSeq)
		return
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
	case err != nil:
		log.Error("bus: final delivery lapsed and was not dead-lettered",
			"durable", adv.Consumer, "stream", adv.Stream, "stream_seq", adv.StreamSeq,
			"deliveries", adv.Deliveries, "msg_id", msgID, "error", err)
	default:
		log.Warn("bus: final delivery lapsed unacknowledged; dead-lettered",
			"durable", adv.Consumer, "stream", adv.Stream, "stream_seq", adv.StreamSeq,
			"deliveries", adv.Deliveries, "msg_id", msgID, "duplicate", dup)
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
