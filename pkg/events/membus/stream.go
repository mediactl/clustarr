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

package membus

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// dedupRecord remembers a published message ID for the stream's duplicate
// window.
type dedupRecord struct {
	seq uint64
	at  time.Time
}

// consumerState is one durable consumer's view of one message.
type consumerState struct {
	// attempts is how many times the message has been handed to this
	// consumer, including the delivery in flight.
	attempts uint64

	// ackDeadline is when an unsettled delivery becomes eligible for
	// redelivery.
	ackDeadline time.Time

	// nextAt is when a negatively acknowledged message may be redelivered.
	nextAt time.Time

	// settled is set once the consumer has acknowledged or terminated the
	// message; it is never delivered to that consumer again.
	settled bool
}

// due reports whether the message would be redelivered to this consumer at
// now: no delivery is in flight inside its acknowledgement deadline and no
// negative acknowledgement is still holding it back.
func (cs *consumerState) due(now time.Time) bool {
	if !cs.ackDeadline.IsZero() && now.Before(cs.ackDeadline) {
		return false
	}
	return cs.nextAt.IsZero() || !now.Before(cs.nextAt)
}

// memMsg is one stored message.
type memMsg struct {
	seq       uint64
	subject   string
	env       *events.Envelope
	deliverAt time.Time
	size      int64

	// removed is set when a WorkQueue-retention message has been settled and
	// is no longer available to anyone.
	removed bool

	// claim is the durable that holds a WorkQueue message. Only that
	// consumer may take it.
	claim string

	state map[string]*consumerState
}

func (m *memMsg) stateFor(durable string) *consumerState {
	if m.state == nil {
		m.state = map[string]*consumerState{}
	}
	cs, ok := m.state[durable]
	if !ok {
		cs = &consumerState{}
		m.state[durable] = cs
	}
	return cs
}

// stream is one in-memory stream.
type stream struct {
	mu    sync.Mutex
	spec  events.StreamSpec
	seq   uint64
	msgs  []*memMsg
	bytes int64
	dedup map[string]dedupRecord
}

// publish appends a message, honouring deduplication and DiscardNew
// back-pressure.
func (s *stream) publish(now time.Time, subject string, env *events.Envelope,
	scheduleAt time.Time,
) (events.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if env.ID != "" && s.spec.Duplicates > 0 {
		if rec, ok := s.dedup[env.ID]; ok && now.Sub(rec.at) < s.spec.Duplicates {
			return events.Receipt{Stream: s.spec.Name, Seq: rec.seq, Duplicate: true}, nil
		}
	}

	size := int64(len(env.Data))
	if s.spec.Discard == events.DiscardNew && s.spec.MaxBytes > 0 &&
		s.bytes+size > s.spec.MaxBytes {
		return events.Receipt{}, events.ErrQueueFull
	}

	s.seq++
	m := &memMsg{
		seq:       s.seq,
		subject:   subject,
		env:       env,
		deliverAt: scheduleAt,
		size:      size,
		state:     map[string]*consumerState{},
	}
	s.msgs = append(s.msgs, m)
	s.bytes += size
	if env.ID != "" {
		s.dedup[env.ID] = dedupRecord{seq: m.seq, at: now}
	}
	s.expireLocked(now)
	return events.Receipt{Stream: s.spec.Name, Seq: m.seq}, nil
}

// expireLocked drops messages past MaxAge and deduplication records past the
// duplicate window. The caller holds s.mu.
func (s *stream) expireLocked(now time.Time) {
	if s.spec.Duplicates > 0 {
		for id, rec := range s.dedup {
			if now.Sub(rec.at) >= s.spec.Duplicates {
				delete(s.dedup, id)
			}
		}
	}
	if s.spec.MaxAge <= 0 {
		return
	}
	keep := s.msgs[:0]
	for _, m := range s.msgs {
		if !m.removed && now.Sub(m.env.Time) >= s.spec.MaxAge {
			m.removed = true
			s.bytes -= m.size
		}
		if !m.removed {
			keep = append(keep, m)
		}
	}
	s.msgs = keep
}

// claim hands the next deliverable message to a durable consumer. It returns
// nil when nothing is ready. A message whose delivery budget is spent is
// never handed out again: its final delivery is still in flight, or lapsed
// is about to dead-letter it.
func (s *stream) claim(durable string, filters []string, now time.Time,
	ackWait func(attempt uint64) time.Duration, maxDeliver int,
) *memMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	workQueue := s.spec.Retention == events.RetentionWorkQueue
	for _, m := range s.msgs {
		if m.removed {
			continue
		}
		if !m.deliverAt.IsZero() && now.Before(m.deliverAt) {
			continue
		}
		if workQueue && m.claim != "" && m.claim != durable {
			continue
		}
		if !matchAny(filters, m.subject) {
			continue
		}
		cs := m.stateFor(durable)
		if cs.settled || !cs.due(now) {
			continue
		}
		if maxDeliver > 0 && cs.attempts >= uint64(maxDeliver) {
			continue
		}
		cs.attempts++
		cs.ackDeadline = now.Add(ackWait(cs.attempts))
		cs.nextAt = time.Time{}
		if workQueue {
			m.claim = durable
		}
		return m
	}
	return nil
}

// lapsed returns, exactly once each, the messages whose final delivery to
// durable has lapsed: the delivery budget is spent and the message would
// otherwise be due again, because its acknowledgement deadline passed with
// the handler still running or because the handler naked it. JetStream gives
// up on such a message on the server, whatever the client is doing, and
// announces it with the MAX_DELIVERIES advisory natsbus dead-letters from;
// this is membus's equivalent. Each message is marked settled for durable,
// and removed from a WorkQueue stream, as a terminated delivery would be.
func (s *stream) lapsed(durable string, filters []string, now time.Time,
	maxDeliver int,
) []*memMsg {
	if maxDeliver <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workQueue := s.spec.Retention == events.RetentionWorkQueue
	var out []*memMsg
	for _, m := range s.msgs {
		if m.removed || !matchAny(filters, m.subject) {
			continue
		}
		if workQueue && m.claim != "" && m.claim != durable {
			continue
		}
		cs, ok := m.state[durable]
		if !ok || cs.settled || cs.attempts < uint64(maxDeliver) || !cs.due(now) {
			continue
		}
		cs.settled = true
		cs.ackDeadline = time.Time{}
		out = append(out, m)
	}
	if workQueue {
		for _, m := range out {
			s.removeLocked(m)
		}
	}
	return out
}

func (s *stream) removeLocked(m *memMsg) {
	if m.removed {
		return
	}
	m.removed = true
	s.bytes -= m.size
	keep := s.msgs[:0]
	for _, x := range s.msgs {
		if !x.removed {
			keep = append(keep, x)
		}
	}
	s.msgs = keep
}

func (s *stream) ack(m *memMsg, durable string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := m.stateFor(durable)
	cs.settled = true
	cs.ackDeadline = time.Time{}
	if s.spec.Retention == events.RetentionWorkQueue {
		s.removeLocked(m)
	}
}

func (s *stream) nak(m *memMsg, durable string, now time.Time, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := m.stateFor(durable)
	cs.ackDeadline = time.Time{}
	cs.nextAt = now.Add(delay)
	if s.spec.Retention == events.RetentionWorkQueue {
		// Release the claim so another replica of the same durable may take
		// it; the durable still owns it, as JetStream does.
		m.claim = durable
	}
}

func (s *stream) inProgress(m *memMsg, durable string, now time.Time,
	ackWait func(attempt uint64) time.Duration,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := m.stateFor(durable)
	cs.ackDeadline = now.Add(ackWait(cs.attempts))
}

// forgetDurable drops every claim and delivery record durable holds on this
// stream: events.StreamAdmin.DeleteSubscription's membus half. A durable that
// never claimed anything is a no-op, matching natsbus deleting an absent
// consumer.
func (s *stream) forgetDurable(durable string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if m.claim == durable {
			m.claim = ""
		}
		delete(m.state, durable)
	}
}

// purgeSubject removes every stored message whose subject equals subject.
func (s *stream) purgeSubject(subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.msgs[:0]
	for _, m := range s.msgs {
		if !m.removed && m.subject == subject {
			m.removed = true
			s.bytes -= m.size
			continue
		}
		keep = append(keep, m)
	}
	s.msgs = keep
}

// subjects returns the sorted distinct stored subjects matching filter.
func (s *stream) subjects(filter string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	for _, m := range s.msgs {
		if m.removed || !events.SubjectMatches(filter, m.subject) {
			continue
		}
		seen[m.subject] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for subj := range seen {
		out = append(out, subj)
	}
	sort.Strings(out)
	return out
}

func matchAny(filters []string, subject string) bool {
	for _, f := range filters {
		if events.SubjectMatches(f, subject) {
			return true
		}
	}
	return false
}

// message adapts a stored message to events.Message.
type message struct {
	bus     *Bus
	stream  *stream
	msg     *memMsg
	durable string
	ackWait func(attempt uint64) time.Duration

	mu      sync.Mutex
	settled bool
}

var _ events.Message = (*message)(nil)

// Envelope returns a copy of the stored envelope, so a handler cannot mutate
// what a redelivery would carry.
func (m *message) Envelope() *events.Envelope { return m.msg.env.Clone() }

// Subject returns the subject the message was published on.
func (m *message) Subject() string { return m.msg.subject }

// Attempt returns the 1-based delivery count.
func (m *message) Attempt() uint64 {
	m.stream.mu.Lock()
	defer m.stream.mu.Unlock()
	return m.msg.stateFor(m.durable).attempts
}

// Ack acknowledges the delivery.
func (m *message) Ack(context.Context) error {
	if !m.markSettled() {
		return nil
	}
	m.stream.ack(m.msg, m.durable)
	return nil
}

// Nak schedules a redelivery.
func (m *message) Nak(_ context.Context, delay time.Duration) error {
	if !m.markSettled() {
		return nil
	}
	m.stream.nak(m.msg, m.durable, m.bus.clock.Now(), delay)
	return nil
}

// Term stops redelivery.
func (m *message) Term(context.Context, string) error {
	if !m.markSettled() {
		return nil
	}
	m.stream.ack(m.msg, m.durable)
	return nil
}

// InProgress extends the acknowledgement deadline.
func (m *message) InProgress(context.Context) error {
	m.stream.inProgress(m.msg, m.durable, m.bus.clock.Now(), m.ackWait)
	return nil
}

// markSettled reports whether this call is the one that settles the message.
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
