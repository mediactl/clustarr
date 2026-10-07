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
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// awaitPoll is how often AwaitTopology asks the bus what is missing (spec
// §3.5.2: every 2 s). It is a variable so a test can shorten it.
var awaitPoll = 2 * time.Second

// AwaitTopology waits at most wait for every stream, consumer, KV bucket and
// object store of t to exist on bus, asking StreamAdmin.Missing every
// awaitPoll. Processes other than the manager call it at start instead of
// EnsureTopology (spec §3.5.2 step 7, §5.9). On timeout it returns
// "waiting for the manager to create <names>" and the process exits 1. The
// kubelet's liveness probe cannot be answered before mgr.Start, so a longer
// wait would end in a kill anyway.
func AwaitTopology(ctx context.Context, bus events.Bus, t events.Topology, wait time.Duration) error {
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return fmt.Errorf("busconn: %T cannot report what of the topology is missing", bus)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(awaitPoll)
	defer tick.Stop()

	var missing []string
	var lastErr error
	for {
		m, err := admin.Missing(ctx, t)
		switch {
		case err == nil && len(m) == 0:
			return nil
		case err == nil:
			missing, lastErr = m, nil
		default:
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if len(missing) > 0 {
				return fmt.Errorf("waiting for the manager to create %s", strings.Join(missing, ", "))
			}
			if lastErr != nil {
				return fmt.Errorf("busconn: waiting for the topology: %w", lastErr)
			}
			return fmt.Errorf("busconn: waiting for the topology: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
