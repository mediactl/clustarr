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
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// message adapts a JetStream message to events.Message.
type message struct {
	jm      jetstream.Msg
	env     *events.Envelope
	attempt uint64

	// backoff is the consumer's BackOff, which changes what a delayed
	// negative acknowledgement asks the server for; see nakDelay.
	backoff []time.Duration

	// onProgress, when set, is told of every InProgress, so the
	// subscription moves the delivery's lapse deadline with the server's.
	onProgress func()

	// lapsed, when set, reports that the subscription has given this
	// delivery's slot back: the broker has redelivered it, and InProgress and
	// Nak, which the server keys by stream sequence, would act on the live
	// copy another handler runs (research D3, E3). Both are muted then; Ack
	// and Term still go -- the work is done, or the message is poison.
	lapsed  func() bool
	durable string // for the muted count's label

	mu      sync.Mutex
	settled bool
}

var _ events.Message = (*message)(nil)

func newMessage(jm jetstream.Msg, backoff []time.Duration, durable string) *message {
	h := make(map[string]string, len(jm.Headers()))
	for k := range jm.Headers() {
		h[k] = jm.Headers().Get(k)
	}
	m := &message{
		jm:      jm,
		env:     events.EnvelopeFromHeaders(h, jm.Data()),
		attempt: 1,
		backoff: backoff,
		durable: durable,
	}
	if md, err := jm.Metadata(); err == nil && md.NumDelivered > 0 {
		m.attempt = md.NumDelivered
	}
	return m
}

// Envelope returns the decoded message.
func (m *message) Envelope() *events.Envelope { return m.env }

// Subject returns the subject the message was published on.
func (m *message) Subject() string { return m.jm.Subject() }

// Attempt returns the server's delivery count for this message.
func (m *message) Attempt() uint64 { return m.attempt }

// Ack acknowledges the delivery and waits for the server to confirm, so a
// work-queue task is never handled twice after a client restart.
func (m *message) Ack(ctx context.Context) error {
	if !m.markSettled() {
		return nil
	}
	return m.jm.DoubleAck(ctx)
}

// Nak schedules a redelivery after delay. A lapsed delivery's Nak is muted:
// it counts as this delivery's settlement, so the bus sends nothing after it,
// but the live copy keeps the broker's schedule.
func (m *message) Nak(ctx context.Context, delay time.Duration) error {
	if !m.markSettled() {
		return nil
	}
	if m.mutedLapsed(ctx, "nak") {
		return nil
	}
	if delay <= 0 {
		return m.jm.Nak()
	}
	return m.jm.NakWithDelay(nakDelay(delay, m.backoff, m.attempt))
}

// nakDelay is the delay to put in a negative acknowledgement of delivery
// attempt so that JetStream redelivers the message want after the nak, on a
// consumer whose BackOff is backoff.
//
// Without BackOff it is want. With it, the server does not wait want. It
// sets the pending entry's timestamp to now - AckWait + want (nats-server
// v2.15.0 server/consumer.go:3302, processNak), then redelivers once the
// entry is BackOff[attempt-1] old, the last entry past the end
// (consumer.go:6169-6180, checkPending, dc = redeliveries so far), and AckWait
// is BackOff[0] because BackOff overrides it (consumer.go:682). The actual
// wait is want + BackOff[attempt-1] - BackOff[0], so every backoff step past
// the first came out close to doubled: catalogarr-search-normal
// ([30s 2m 10m 1h]) waited nearly two hours, not one, after its fourth
// attempt. Subtracting the server's addition asks for exactly want.
//
// When want is shorter than the addition, the server cannot redeliver that
// soon after a delayed nak, and a plain nak would redeliver at once, sooner
// than asked. A retry delay is a floor (a backing-off indexer, a rate limit),
// so the result is floored at the smallest delay that is still a delayed
// nak, and the wait is the addition: the nearest the server allows without
// being early. events.Settle never asks for less: its want is
// BackOff[attempt-1] itself, which comes out as BackOff[0] here.
func nakDelay(want time.Duration, backoff []time.Duration, attempt uint64) time.Duration {
	if len(backoff) == 0 {
		return want
	}
	i := 0
	if attempt > 1 {
		i = int(min(attempt-1, uint64(len(backoff)-1)))
	}
	return max(want-(backoff[i]-backoff[0]), time.Nanosecond)
}

// Term stops redelivery and records reason in the server advisory. The
// reason carries the message's Clustarr-Id first, then one space (ADR-0019
// §8.2, ruling R10): on a WorkQueue stream a terminated message is gone, so
// the manager's advisory intake resolves the task from the reason's first
// field.
func (m *message) Term(_ context.Context, reason string) error {
	if !m.markSettled() {
		return nil
	}
	reason = termReason(m.clustarrID(), reason)
	if reason == "" {
		return m.jm.Term()
	}
	return m.jm.TermWithReason(reason)
}

// clustarrID is the delivery's Clustarr-Id header, else its envelope's ID
// (Nats-Msg-Id): the same value for every clustarr publish.
func (m *message) clustarrID() string {
	if h := m.jm.Headers(); h != nil {
		if id := h.Get(events.HeaderID); id != "" {
			return id
		}
	}
	if m.env != nil {
		return m.env.ID
	}
	return ""
}

// termReason is "<id> <reason>", or id alone with no reason, or reason
// alone with no id.
func termReason(id, reason string) string {
	switch {
	case id == "":
		return reason
	case reason == "":
		return id
	}
	return id + " " + reason
}

// InProgress resets the server's redelivery timer for this delivery, and the
// subscription's lapse deadline with it. A lapsed delivery's is muted.
func (m *message) InProgress(ctx context.Context) error {
	if m.mutedLapsed(ctx, "in_progress") {
		return nil
	}
	if err := m.jm.InProgress(); err != nil {
		return err
	}
	if m.onProgress != nil {
		m.onProgress()
	}
	return nil
}

// mutedLapsed reports, and counts, an op the bus does not send because this
// delivery has lapsed (split §9.3 as amended 2026-10-07, S2).
func (m *message) mutedLapsed(ctx context.Context, op string) bool {
	if m.lapsed == nil || !m.lapsed() {
		return false
	}
	metrics.BusMutedTotal.WithLabelValues(m.durable, op).Inc()
	logging.FromContext(ctx).Debug("bus: a lapsed delivery's settlement was not sent",
		"durable", m.durable, "op", op)
	return true
}

func (m *message) markSettled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settled {
		return false
	}
	m.settled = true
	return true
}

func (m *message) settledByHandler() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settled
}
