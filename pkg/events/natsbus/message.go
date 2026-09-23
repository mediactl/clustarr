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
)

// message adapts a JetStream message to events.Message.
type message struct {
	jm      jetstream.Msg
	env     *events.Envelope
	attempt uint64

	// backoff is the consumer's BackOff, which changes what a delayed
	// negative acknowledgement asks the server for; see nakDelay.
	backoff []time.Duration

	mu      sync.Mutex
	settled bool
}

var _ events.Message = (*message)(nil)

func newMessage(jm jetstream.Msg, backoff []time.Duration) *message {
	h := make(map[string]string, len(jm.Headers()))
	for k := range jm.Headers() {
		h[k] = jm.Headers().Get(k)
	}
	m := &message{
		jm:      jm,
		env:     events.EnvelopeFromHeaders(h, jm.Data()),
		attempt: 1,
		backoff: backoff,
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

// Nak schedules a redelivery after delay.
func (m *message) Nak(_ context.Context, delay time.Duration) error {
	if !m.markSettled() {
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

// Term stops redelivery and records reason in the server advisory.
func (m *message) Term(_ context.Context, reason string) error {
	if !m.markSettled() {
		return nil
	}
	if reason == "" {
		return m.jm.Term()
	}
	return m.jm.TermWithReason(reason)
}

// InProgress resets the server's redelivery timer for this delivery.
func (m *message) InProgress(context.Context) error { return m.jm.InProgress() }

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
