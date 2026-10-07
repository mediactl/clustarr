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
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// keepInterval is how often KeepTopology asks what is missing.
var keepInterval = 60 * time.Second

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
func KeepTopology(ctx context.Context, nc *nats.Conn, bus events.Bus, t events.Topology) error {
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return fmt.Errorf("busconn: %T cannot report missing topology (no events.StreamAdmin)", bus)
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
