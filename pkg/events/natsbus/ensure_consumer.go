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
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

// EnsureConsumer implements events.StreamAdmin: CreateOrUpdateConsumer for c
// on c.Stream, then for its dead-letter watcher on StreamAdvisories, so a
// redelivered final delivery of a dynamic durable (an engine instance's) is
// dead-lettered like any topology durable's. Both writes are idempotent.
// Only the manager calls it (ADR-0019 §8.5, ruling R15).
func (b *Bus) EnsureConsumer(ctx context.Context, c events.ConsumerSpec) error {
	if c.Name == "" || c.Stream == "" || len(c.Filters) == 0 {
		return fmt.Errorf("natsbus: ensure consumer %q on %q: a name, a stream and a filter are required", c.Name, c.Stream)
	}
	for _, spec := range []events.ConsumerSpec{c, events.DeadLetterWatcherSpec(c)} {
		if _, err := b.js.CreateOrUpdateConsumer(ctx, spec.Stream, events.ConsumerConfig(spec)); err != nil {
			if errors.Is(err, jetstream.ErrStreamNotFound) {
				return fmt.Errorf("natsbus: ensure consumer %s on %s: %w", spec.Name, spec.Stream, events.ErrStreamNotFound)
			}
			return fmt.Errorf("natsbus: ensure consumer %s on %s: %w", spec.Name, spec.Stream, err)
		}
	}
	return nil
}
