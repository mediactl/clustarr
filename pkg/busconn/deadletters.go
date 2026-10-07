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

	"github.com/mediactl/clustarr/pkg/events"
)

// WatchDeadLetters watches every static consumer of t for lapsed final
// deliveries until ctx ends: the manager's leader-only backstop (spec §5.9).
// A lapsed final delivery stays ack-pending until JetStream raises its
// MAX_DELIVERIES advisory on a waiting pull, which a draining pod's last pull
// can do after the pod is gone; in a domain at zero replicas nothing else
// would copy it until the next wake. The dead-letter watchers themselves
// (on StreamAdvisories) are not watched.
func WatchDeadLetters(ctx context.Context, bus events.Bus, t events.Topology) error {
	w, ok := bus.(events.DeadLetterWatcher)
	if !ok {
		return fmt.Errorf("busconn: %T is not an events.DeadLetterWatcher", bus)
	}
	var stops []func()
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	for _, c := range t.Consumers {
		if c.Stream == events.StreamAdvisories {
			continue
		}
		stop, err := w.WatchDeadLetters(ctx, c.Subscription())
		if err != nil {
			return fmt.Errorf("busconn: watch the dead letters of %s: %w", c.Name, err)
		}
		stops = append(stops, stop)
	}
	<-ctx.Done()
	return nil
}
