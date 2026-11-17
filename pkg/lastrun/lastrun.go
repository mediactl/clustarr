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

// Package lastrun remembers when a periodic job last finished, so that a
// process that restarts does not run it again at once.
//
// The manager's sweeps of the object stores (app/maintenance, the artwork
// reaper) ran at every start as well as every six hours, and each cost the
// NATS servers a read of a whole store: fourteen manager restarts on
// 2026-11-16 were fourteen sweeps. A job that records its end here waits out
// what is left of its interval at the next start.
package lastrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// Mark is one job's record: the time it last finished, under Key in KV. A
// nil Mark remembers nothing: the job is always due.
type Mark struct {
	// KV is the bucket; the manager's is events.BucketMaintenance.
	KV events.KV
	// Key names the job. It must be a legal KV key.
	Key string
}

// Wait is how long from now the job is next due when it runs every
// interval: nothing when it never finished, when its record cannot be read
// or parsed -- running early costs one run, never running costs the job --
// and when the interval has passed. A record from the future, a clock that
// stepped back, waits one interval at most.
func (m *Mark) Wait(ctx context.Context, now time.Time, interval time.Duration) time.Duration {
	if m == nil || m.KV == nil || interval <= 0 {
		return 0
	}
	e, err := m.KV.Get(ctx, m.Key)
	if err != nil {
		return 0
	}
	last, err := time.Parse(time.RFC3339Nano, string(e.Value))
	if err != nil {
		return 0
	}
	return min(max(last.Add(interval).Sub(now), 0), interval)
}

// Done records that the job finished at now.
func (m *Mark) Done(ctx context.Context, now time.Time) error {
	if m == nil || m.KV == nil {
		return nil
	}
	if _, err := m.KV.Put(ctx, m.Key, []byte(now.UTC().Format(time.RFC3339Nano))); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("lastrun: record %s: %w", m.Key, err)
	}
	return nil
}
