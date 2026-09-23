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
	"sort"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.StreamAdmin = (*Bus)(nil)

// DeleteSubscription implements events.StreamAdmin.
func (b *Bus) DeleteSubscription(ctx context.Context, stream, durable string) error {
	for _, c := range [][2]string{{stream, durable}, {events.StreamAdvisories, dlqWatchName(stream, durable)}} {
		err := b.js.DeleteConsumer(ctx, c[0], c[1])
		if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("natsbus: delete consumer %s/%s: %w", c[0], c[1], err)
		}
	}
	return nil
}

// PurgeSubject implements events.StreamAdmin.
func (b *Bus) PurgeSubject(ctx context.Context, stream, subject string) error {
	st, err := b.js.Stream(ctx, stream)
	if err != nil {
		return fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	if err := st.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return fmt.Errorf("natsbus: purge %s on %s: %w", subject, stream, err)
	}
	return nil
}

// Subjects implements events.StreamAdmin.
func (b *Bus) Subjects(ctx context.Context, stream, filter string) ([]string, error) {
	st, err := b.js.Stream(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s subjects %s: %w", stream, filter, err)
	}
	out := make([]string, 0, len(info.State.Subjects))
	for s := range info.State.Subjects {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}
