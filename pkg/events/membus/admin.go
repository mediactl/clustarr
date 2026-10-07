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
	"fmt"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.StreamAdmin = (*Bus)(nil)

// DeleteSubscription implements events.StreamAdmin. A stream that was never
// ensured, or a durable that never claimed anything on it, is not an error:
// idempotent, matching natsbus deleting an absent consumer.
func (b *Bus) DeleteSubscription(_ context.Context, stream, durable string) error {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return nil
	}
	st.forgetDurable(durable)
	return nil
}

// EnsureConsumer implements events.StreamAdmin: it records c, and its
// dead-letter watcher on StreamAdvisories, as existing with their filters and
// caps, as natsbus creates both. SampleFrequency is ignored. A stream not yet
// ensured is ErrStreamNotFound.
func (b *Bus) EnsureConsumer(_ context.Context, c events.ConsumerSpec) error {
	if c.Name == "" || c.Stream == "" || len(c.Filters) == 0 {
		return fmt.Errorf("membus: ensure consumer %q on %q: a name, a stream and a filter are required", c.Name, c.Stream)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return events.ErrClosed
	}
	specs := []events.ConsumerSpec{c, events.DeadLetterWatcherSpec(c)}
	for _, spec := range specs {
		if b.streams[spec.Stream] == nil {
			return fmt.Errorf("membus: ensure consumer %s on %s: %w", spec.Name, spec.Stream, events.ErrStreamNotFound)
		}
	}
	for _, spec := range specs {
		b.streams[spec.Stream].bindDurable(spec.Name, spec.Filters, spec.MaxAckPending, spec.Subscription().Timing())
	}
	return nil
}

// PurgeSubject implements events.StreamAdmin.
func (b *Bus) PurgeSubject(_ context.Context, stream, subject string) error {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	st.purgeSubject(subject)
	return nil
}

// Subscriptions implements events.StreamAdmin.
func (b *Bus) Subscriptions(_ context.Context, stream string) ([]string, error) {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return nil, fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	return st.subscriptions(), nil
}

// ConsumerState implements events.StreamAdmin.
func (b *Bus) ConsumerState(_ context.Context, stream, durable string) (events.ConsumerState, error) {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return events.ConsumerState{}, fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	cs, ok := st.consumerState(durable, b.clock.Now())
	if !ok {
		return events.ConsumerState{}, fmt.Errorf("membus: consumer %s on %s: %w", durable, stream, events.ErrConsumerNotFound)
	}
	cs.Waiting = b.freeSlots(stream, durable)
	return cs, nil
}

// Message implements events.StreamAdmin: the message still stored at seq.
func (b *Bus) Message(_ context.Context, stream string, seq uint64) (string, *events.Envelope, error) {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return "", nil, fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	subject, env, ok := st.message(seq)
	if !ok {
		return "", nil, fmt.Errorf("membus: %s seq %d: %w", stream, seq, events.ErrMessageNotFound)
	}
	return subject, env, nil
}

var _ events.StreamStater = (*Bus)(nil)

// StreamFill implements events.StreamStater: the stored messages' payload and
// header bytes, against the spec's MaxBytes.
func (b *Bus) StreamFill(_ context.Context, stream string) (events.StreamFill, error) {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return events.StreamFill{}, fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	return st.fill(), nil
}

// Subjects implements events.StreamAdmin.
func (b *Bus) Subjects(_ context.Context, stream, filter string) ([]string, error) {
	b.mu.Lock()
	st := b.streams[stream]
	b.mu.Unlock()
	if st == nil {
		return nil, fmt.Errorf("membus: stream %s: %w", stream, events.ErrStreamNotFound)
	}
	return st.subjects(filter), nil
}

// Missing implements events.StreamAdmin.
func (b *Bus) Missing(_ context.Context, t events.Topology) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, events.ErrClosed
	}
	var out []string
	for _, o := range t.Objects() {
		var ok bool
		switch o.Kind {
		case events.TopologyStream:
			_, ok = b.streams[o.Name]
		case events.TopologyConsumer:
			st := b.streams[o.Stream]
			ok = st != nil && st.hasDurable(o.Name)
		case events.TopologyBucket:
			_, ok = b.buckets[o.Name]
		case events.TopologyObjectStore:
			_, ok = b.objectStores[o.Name]
		}
		if !ok {
			out = append(out, o.String())
		}
	}
	return out, nil
}
