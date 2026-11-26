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

package readwrite

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// FeedRetention is how long a change stays in the library's feed. A
	// watcher further behind than this lists again, which costs one read of
	// its kind; nothing is lost.
	FeedRetention = time.Hour
	// trimInterval is how often the leader trims the feed.
	trimInterval = 10 * time.Minute
)

// TrimFeed trims the library's change feed until ctx ends: the manager's
// leader's, since one trimmer is enough and the trim is a write.
func TrimFeed(ctx context.Context, store *library.Store) error {
	ctx = logging.Subsystem(ctx, "library.feed")
	log := logging.FromContext(ctx)
	t := time.NewTicker(trimInterval)
	defer t.Stop()
	for {
		if err := store.Trim(ctx, FeedRetention); err != nil && ctx.Err() == nil {
			log.Warn("trimming the library's change feed failed; it is tried again", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
