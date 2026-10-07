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

	// deadLettered is set when lapsed gave up on the final delivery: the
	// message is settled, but a handler may still be running it, and that
	// handler's slot is reclaimed as natsbus reclaims any lapsed delivery's
	// (overdue).
	deadLettered bool
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

	// durables is every durable Ensure, a Subscribe or a Pull has bound on
	// this stream and no DeleteSubscription has forgotten since: JetStream's
	// consumer list, which events.StreamAdmin.Subscriptions reports, with
	// what membus enforces of each one's config. A JetStream durable outlives
	// the subscription that created it, and so does an entry here.
	durables map[string]durableState
}

// durableState is what Ensure (or Pull) declared for one durable, as far as
// membus enforces JetStream's consumer config: its filters and its cap on
// unsettled deliveries across every subscription and puller.
type durableState struct {
	filters       []string
	maxAckPending int
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
// nil when nothing is ready, and for a durable not bound on the stream: a
// deleted durable takes nothing until it is bound again. A message whose
// delivery budget is spent is never handed out again: its final delivery is
// still in flight, or lapsed is about to dead-letter it.
func (s *stream) claim(durable string, filters []string, now time.Time,
	ackWait func(attempt uint64) time.Duration, maxDeliver int,
) *memMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, bound := s.durables[durable]
	if !bound {
		return nil
	}
	workQueue := s.spec.Retention == events.RetentionWorkQueue
	capped := s.unsettledLocked(durable) >= d.maxAckPending
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
		// At the cap JetStream still redelivers what is already ack-pending
		// (a redelivery takes no new place under MaxAckPending); it only
		// stops making first deliveries.
		if capped && cs.attempts == 0 {
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
		cs.deadLettered = true
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

// attemptOf is durable's delivery count of m.
func (s *stream) attemptOf(m *memMsg, durable string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return m.stateFor(durable).attempts
}

// overdue reports whether durable's delivery attempt of m is past its
// acknowledgement deadline, or has been made again since. A final delivery
// lapsed has dead-lettered is overdue too: its handler may still be running,
// and natsbus frees that handler's slot at the deadline whatever the
// advisory watcher has done.
func (s *stream) overdue(m *memMsg, durable string, attempt uint64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := m.state[durable]
	if !ok {
		return false
	}
	if cs.settled {
		return cs.deadLettered && cs.attempts == attempt
	}
	return cs.attempts != attempt || (!cs.ackDeadline.IsZero() && !now.Before(cs.ackDeadline))
}

func (s *stream) inProgress(m *memMsg, durable string, now time.Time,
	ackWait func(attempt uint64) time.Duration,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := m.stateFor(durable)
	cs.ackDeadline = now.Add(ackWait(cs.attempts))
}

// forgetDurable drops durable from this stream, with every claim and
// delivery record it holds: events.StreamAdmin.DeleteSubscription's membus
// half. An unknown durable is a no-op, matching natsbus deleting an absent
// consumer.
func (s *stream) forgetDurable(durable string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.durables, durable)
	for _, m := range s.msgs {
		if m.claim == durable {
			m.claim = ""
		}
		delete(m.state, durable)
	}
}

// bindDurable records durable as existing on this stream with its filters
// and its cap on unsettled deliveries, as JetStream creates a consumer from
// Ensure's topology or on a Pull.
func (s *stream) bindDurable(durable string, filters []string, maxAckPending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.durables == nil {
		s.durables = map[string]durableState{}
	}
	s.durables[durable] = durableState{
		filters:       append([]string(nil), filters...),
		maxAckPending: max(maxAckPending, 1),
	}
}

// unsettledLocked counts durable's deliveries neither acknowledged nor
// terminated: JetStream's NumAckPending. The caller holds s.mu.
func (s *stream) unsettledLocked(durable string) int {
	n := 0
	for _, m := range s.msgs {
		if cs, ok := m.state[durable]; ok && !m.removed && cs.attempts > 0 && !cs.settled {
			n++
		}
	}
	return n
}

// consumerState is durable's ConsumerState, false when durable is not bound.
func (s *stream) consumerState(durable string, now time.Time) (events.ConsumerState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.durables[durable]
	if !ok {
		return events.ConsumerState{}, false
	}
	workQueue := s.spec.Retention == events.RetentionWorkQueue
	out := events.ConsumerState{MaxAckPending: d.maxAckPending, ObservedAt: now}
	for _, m := range s.msgs {
		if m.removed || !matchAny(d.filters, m.subject) {
			continue
		}
		if !m.deliverAt.IsZero() && now.Before(m.deliverAt) {
			continue
		}
		if workQueue && m.claim != "" && m.claim != durable {
			continue
		}
		switch cs, ok := m.state[durable]; {
		case !ok || cs.attempts == 0:
			out.Pending++
		case !cs.settled:
			out.AckPending++
		}
	}
	return out, true
}

// hasDurable reports whether durable exists on this stream: bound by Ensure,
// Subscribe or Pull, and not forgotten since.
func (s *stream) hasDurable(durable string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.durables[durable]
	return ok
}

// subscriptions returns the sorted durables bound on this stream.
func (s *stream) subscriptions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.durables))
	for d := range s.durables {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// purgeSubject removes every stored message whose subject matches subject,
// which may itself be a wildcard filter (events.StreamAdmin.PurgeSubject):
// the same matcher subjects(filter) uses below, so "*" and a trailing ">"
// work here exactly as they do there and as natsbus's real PurgeSubject
// (jetstream.WithPurgeSubject) honours them.
func (s *stream) purgeSubject(subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.msgs[:0]
	for _, m := range s.msgs {
		if !m.removed && events.SubjectMatches(subject, m.subject) {
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

	// lapsed, when set, reports that the subscription has given this
	// delivery's slot back: InProgress and Nak, keyed by message as the
	// broker keys them by stream sequence, would act on the live copy, so
	// both are muted (natsbus's message.lapsed).
	lapsed func() bool

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

// Nak schedules a redelivery. A lapsed delivery's is muted.
func (m *message) Nak(_ context.Context, delay time.Duration) error {
	if !m.markSettled() {
		return nil
	}
	if m.lapsed != nil && m.lapsed() {
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

// InProgress extends the acknowledgement deadline. A lapsed delivery's is
// muted.
func (m *message) InProgress(context.Context) error {
	if m.lapsed != nil && m.lapsed() {
		return nil
	}
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
