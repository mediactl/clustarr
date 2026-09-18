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

package grab

import (
	"context"
	"errors"
	"fmt"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ErrDuplicateGrab reports that some other grab already owns this media key.
// Spec §8.2's rule for it is "on exists -> ack and stop": it is a normal
// outcome of at-least-once delivery and of two workers racing, not a failure,
// so a work handler must acknowledge it rather than nak.
var ErrDuplicateGrab = errors.New("grab: lease already held")

// acquireLeases takes the clustarr-leases key for every status target of one
// grab, all or nothing.
//
// All-or-nothing is what makes a season pack safe. A pack covering episodes
// 1-10 where episode 4 is already downloading must not half-grab: spec §8.2
// says "for every key (episodes of a pack; all-or-nothing, release taken ones
// on failure)". Partial success would leave a Download that overlaps another
// one, and leases the loser would never clean up.
//
// The primitive is Create-fails-if-exists, which is atomic at the broker, so
// two workers racing over the same key cannot both win. The value is the
// Download name, so an operator reading the bucket can see which grab holds
// what, and so grabarr can match the lease to the Download it releases.
//
// Leases taken by a winning grab are deliberately NOT released here on
// success. They outlive this function: grabarr deletes them when the Download
// reaches a terminal phase, and a 10-minute sweeper reclaims a key whose
// Download no longer exists (spec §5's clustarr-leases row).
func acquireLeases(ctx context.Context, kv events.KV, keys []string, downloadName string) ([]string, error) {
	acquired := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, err := kv.Create(ctx, key, []byte(downloadName)); err != nil {
			releaseLeases(ctx, kv, acquired)
			if errors.Is(err, events.ErrKeyExists) {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateGrab, key)
			}
			return nil, fmt.Errorf("grab: take lease %q: %w", key, err)
		}
		acquired = append(acquired, key)
	}
	return acquired, nil
}

// releaseLeases deletes every key in acquired, best effort. A failed delete is
// logged and skipped rather than returned: this runs on the rollback path of a
// grab that is already failing, and turning a cleanup error into the caller's
// error would mask the real cause. A lease left behind by a failed delete is
// not permanent either -- §5's 10-minute sweeper reclaims a key whose Download
// does not exist.
func releaseLeases(ctx context.Context, kv events.KV, acquired []string) {
	for _, key := range acquired {
		if err := kv.Delete(ctx, key); err != nil {
			logging.FromContext(ctx).Warn("grab: releasing lease failed; the sweeper will reclaim it",
				"key", key, "error", err)
		}
	}
}
