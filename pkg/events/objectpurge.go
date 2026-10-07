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

package events

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OrphanPurge is one object store's PurgeOrphanChunks outcome.
type OrphanPurge struct {
	Bucket   string
	Subjects int
	Bytes    uint64
}

// ObjectStoreNames lists the names of every object store of t, in order: the
// list the orphan purge walks, so a store added to the topology later is
// purged without anyone remembering to (ADR-0020 §5).
func (t Topology) ObjectStoreNames() []string {
	out := make([]string, 0, len(t.ObjectStores))
	for _, o := range t.ObjectStores {
		out = append(out, o.Name)
	}
	return out
}

// PurgeOrphans is the one orphan-chunk purge (ADR-0020 §5): it runs
// admin.PurgeOrphanChunks over every bucket with grace, which covers a Put in
// progress (its chunks precede its meta). The manager's maintenance loop
// calls it over every object store of the topology
// (Topology.ObjectStoreNames); the artwork reaper calls it over its own
// bucket. A bucket this deployment never created is skipped
// (ErrBucketNotFound, ErrStreamNotFound); every other failure is joined into
// the error, and the pass carries on past it. It returns every bucket's
// outcome, purged or not.
func PurgeOrphans(ctx context.Context, admin ObjectStoreAdmin, buckets []string, grace time.Duration) ([]OrphanPurge, error) {
	if admin == nil {
		return nil, nil
	}
	out := make([]OrphanPurge, 0, len(buckets))
	var errs []error
	for _, b := range buckets {
		n, freed, err := admin.PurgeOrphanChunks(ctx, b, grace)
		if err != nil && !errors.Is(err, ErrBucketNotFound) && !errors.Is(err, ErrStreamNotFound) {
			errs = append(errs, fmt.Errorf("events: purge orphaned chunks of %s: %w", b, err))
		}
		out = append(out, OrphanPurge{Bucket: b, Subjects: n, Bytes: freed})
	}
	return out, errors.Join(errs...)
}
