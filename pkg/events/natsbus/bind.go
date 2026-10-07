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
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// How a subscription waits for a durable it binds: the manager creates it
// (spec §5.9), so one that is missing -- not created yet, or wiped by a NATS
// restart -- is polled for until ctx ends, with a warning once a minute.
const (
	bindPoll      = 5 * time.Second
	bindWarnEvery = time.Minute
)

// bindConsumer looks durable up on stream and waits while it is missing. It
// never creates or updates it. Beside the consumer it returns the durable's
// timing as the broker stores it, which a subscription times and settles on
// (S5). The error is ctx's.
func (b *Bus) bindConsumer(ctx context.Context, stream, durable string) (jetstream.Consumer, events.Timing, error) {
	var warned time.Time
	for {
		c, err := b.js.Consumer(ctx, stream, durable)
		if err == nil {
			cfg := c.CachedInfo().Config
			return c, events.Timing{AckWait: cfg.AckWait, Backoff: cfg.BackOff, MaxDeliver: cfg.MaxDeliver}, nil
		}
		if ctx.Err() != nil {
			return nil, events.Timing{}, ctx.Err()
		}
		if time.Since(warned) >= bindWarnEvery {
			logging.FromContext(ctx).Warn("bus: waiting for a durable the manager creates",
				"stream", stream, "durable", durable, "error", lookupError(stream, durable, err))
			warned = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, events.Timing{}, ctx.Err()
		case <-time.After(bindPoll):
		}
	}
}

// consumerGone reports a Fetch failure that means the durable or its stream
// no longer exists, so the loop must look it up again rather than retry the
// handle. nats.go v1.53.1 reports a consumer deleted under a waiting pull as
// ErrConsumerDeleted (409), a pull on a consumer that does not exist as
// nats.ErrNoResponders (503, jetstream/message.go checkMsg), and a server
// that restarted under a pull as ErrNoHeartbeat.
func consumerGone(err error) bool {
	return errors.Is(err, jetstream.ErrConsumerDeleted) ||
		errors.Is(err, jetstream.ErrConsumerNotFound) ||
		errors.Is(err, jetstream.ErrStreamNotFound) ||
		errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, jetstream.ErrNoHeartbeat)
}

// benignFetchError is a Fetch that ended without messages: it expired, or
// its context ended.
func benignFetchError(err error) bool {
	return errors.Is(err, nats.ErrTimeout) || errors.Is(err, jetstream.ErrNoMessages) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
