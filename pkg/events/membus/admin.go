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
