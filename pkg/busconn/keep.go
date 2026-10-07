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

package busconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// keepInterval is how often KeepTopology asks what is missing.
var keepInterval = 60 * time.Second

// KeepOption configures KeepTopology.
type KeepOption func(*keepOptions)

type keepOptions struct {
	onRetired func(events.StreamCensus)
}

// OnRetired hands f the census of every displaced legacy stream
// KeepTopology retires (unbranded-names spec §4.2.1); F8.3 wires it to the
// Migrator's retire_stream outcome metric.
func OnRetired(f func(events.StreamCensus)) KeepOption {
	return func(o *keepOptions) { o.onRetired = f }
}

// KeepTopology keeps t on the broker while ctx lives: the manager's
// leader-only runnable (spec §5.9). After the split only the manager creates
// topology, and on single-node NATS every stream and bucket ForSingleNode
// keeps in memory -- with its consumers -- is gone after a NATS restart. So
// it ensures t once at start, when the lease is won (converging anything an
// older leader changed meanwhile), again on every reconnect of nc, and every
// keepInterval while StreamAdmin.Missing reports anything. A failed ensure is
// logged and retried, never returned: the next reconnect or interval tries
// again. Running subscriptions re-bind on their own. nc may be nil (no
// reconnect hook, as for membus).
//
// Before each ensure it retires every displaced legacy stream that still
// exists (t.Displaced, unbranded-names spec §4.2.1): it reads and logs the
// stream's census, hands it to the OnRetired hook, deletes the stream (its
// durables go with it), then deletes those durables' dead-letter watchers
// on CLUSTARR_ADVISORIES and purges their advisory subjects -- so the
// ensure that follows creates the stream's replacement. Only the leader
// runs this: the legacy-lease gate has opened, so no chart 0.4.x writer is
// left. A failure is logged and retried at the next ensure; the start-up
// EnsureTopology never deletes anything.
func KeepTopology(ctx context.Context, nc *nats.Conn, bus events.Bus, t events.Topology, opts ...KeepOption) error {
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return fmt.Errorf("busconn: %T cannot report missing topology (no events.StreamAdmin)", bus)
	}
	var o keepOptions
	for _, opt := range opts {
		opt(&o)
	}
	log := logging.FromContext(ctx)
	reconnected := make(chan struct{}, 1)
	if nc != nil {
		// Chain onto whatever reconnect handler the connection already has,
		// and put it back when the lease is lost.
		prev := nc.ReconnectHandler()
		nc.SetReconnectHandler(func(c *nats.Conn) {
			if prev != nil {
				prev(c)
			}
			select {
			case reconnected <- struct{}{}:
			default:
			}
		})
		defer nc.SetReconnectHandler(prev)
	}
	ensure := func(why string) {
		retireDisplaced(ctx, admin, t.Displaced, o.onRetired)
		if err := EnsureTopology(ctx, bus, t); err != nil {
			if ctx.Err() == nil {
				log.Error("bus: ensuring the topology failed; will retry", "why", why, "error", err)
			}
			return
		}
		log.Info("bus: topology ensured", "why", why)
	}
	ensure("lease acquired")
	tick := time.NewTicker(keepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-reconnected:
			ensure("NATS reconnected")
		case <-tick.C:
			gone, err := admin.Missing(ctx, t)
			switch {
			case err != nil:
				if ctx.Err() == nil {
					log.Warn("bus: could not ask what topology is missing", "error", err)
				}
			case len(gone) > 0:
				log.Warn("bus: topology objects missing; re-creating them", "missing", gone)
				ensure("objects missing")
			}
		}
	}
}

// retireDisplaced retires each displaced pair's Old stream and its
// durables' dead-letter watchers (see KeepTopology). It logs and carries
// on past a failure; the next ensure retries.
func retireDisplaced(ctx context.Context, admin events.StreamAdmin, displaced []events.DisplacedStream, onRetired func(events.StreamCensus)) {
	if len(displaced) == 0 {
		return
	}
	log := logging.FromContext(ctx)
	watchers, err := admin.Subscriptions(ctx, events.StreamAdvisories)
	if errors.Is(err, events.ErrStreamNotFound) {
		watchers, err = nil, nil
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Error("bus: could not list the dead-letter watchers to retire displaced streams; will retry", "error", err)
		}
		return
	}
	for _, d := range displaced {
		census, err := admin.RetireStream(ctx, d.Old)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("bus: retiring a displaced legacy stream failed; will retry", "stream", d.Old, "replacement", d.New, "error", err)
			}
			continue
		}
		if census.Existed {
			log.Warn("bus: retired a displaced legacy stream; its queued work is re-derived, not copied",
				"stream", d.Old, "replacement", d.New, "messages", census.Messages,
				"bySubjectToken", census.BySubjectToken, "pending", census.Pending)
			if onRetired != nil {
				onRetired(census)
			}
		}
		// Every durable that lived on Old -- the renamed and kept static
		// ones and the dynamic pool durables alike -- has its watcher named
		// DeadLetterWatcherName(Old, durable).
		prefix := events.DeadLetterWatcherName(d.Old, "")
		var found bool
		for _, w := range watchers {
			durable, ok := strings.CutPrefix(w, prefix)
			if !ok || durable == "" {
				continue
			}
			found = true
			if err := admin.DeleteSubscription(ctx, d.Old, durable); err != nil && ctx.Err() == nil {
				log.Error("bus: deleting a displaced durable's dead-letter watcher failed; will retry", "watcher", w, "error", err)
			}
		}
		if census.Existed || found {
			if err := admin.PurgeSubject(ctx, events.StreamAdvisories, events.MaxDeliveriesAdvisorySubject(d.Old, "*")); err != nil &&
				!errors.Is(err, events.ErrStreamNotFound) && ctx.Err() == nil {
				log.Error("bus: purging a displaced stream's advisories failed; will retry", "stream", d.Old, "error", err)
			}
		}
	}
}
