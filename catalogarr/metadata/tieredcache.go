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

package metadata

import (
	"context"
	"time"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// tieredCache is metadata.Cache over the two tiers §6.1 describes: an
// in-process LRU in front of the shared KV bucket. ADR-0007 pins the
// gateway to one replica, so "L1" and "the process cache" are the same
// thing; L2 is what survives a restart.
type tieredCache struct {
	l1 pkgmetadata.Cache
	l2 pkgmetadata.Cache
}

func newTieredCache(l1, l2 pkgmetadata.Cache) *tieredCache {
	return &tieredCache{l1: l1, l2: l2}
}

// Get checks L1 first, then L2, backfilling L1 on an L2 hit so the next
// request in this process skips the KV round trip. Every check increments
// metrics.MetadataCacheHitsTotal, labelled by which tier was checked (l1 is
// checked on every call; l2 only when l1 misses) and whether that tier had
// the entry — never by the key itself, which could carry an unbounded
// title or id.
func (c *tieredCache) Get(ctx context.Context, key string, out any) (bool, error) {
	hit, err := c.l1.Get(ctx, key, out)
	if err != nil {
		return false, err
	}
	if hit {
		metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "hit").Inc()
		return true, nil
	}
	metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "miss").Inc()

	hit, err = c.l2.Get(ctx, key, out)
	if err != nil {
		return false, err
	}
	if !hit {
		metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "miss").Inc()
		return false, nil
	}
	metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "hit").Inc()
	// Backfill L1 so the next request in this process skips the KV round
	// trip. A backfill failure must not fail the read that just succeeded.
	_ = c.l1.Set(ctx, key, out, time.Hour)
	return true, nil
}

func (c *tieredCache) Set(ctx context.Context, key string, v any, ttl time.Duration) error {
	if err := c.l2.Set(ctx, key, v, ttl); err != nil {
		return err
	}
	return c.l1.Set(ctx, key, v, ttl)
}

var _ pkgmetadata.Cache = (*tieredCache)(nil)
