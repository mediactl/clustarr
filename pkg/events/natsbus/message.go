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

	mu      sync.Mutex
	settled bool
}

var _ events.Message = (*message)(nil)

func newMessage(jm jetstream.Msg) *message {
	h := make(map[string]string, len(jm.Headers()))
	for k := range jm.Headers() {
		h[k] = jm.Headers().Get(k)
	}
	m := &message{
		jm:      jm,
		env:     events.EnvelopeFromHeaders(h, jm.Data()),
		attempt: 1,
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
	return m.jm.NakWithDelay(delay)
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
