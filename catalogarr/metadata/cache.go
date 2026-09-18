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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// kvEntry is what kvCache stores in the bucket. The bucket's own 30d TTL
// (events.Default()'s BucketMetadataCache) is a hard backstop; per-entry
// freshness is decided here by ExpiresAt, exactly as §5 documents ("per-entry
// expiry checked by gateway").
type kvEntry struct {
	Value     json.RawMessage `json:"value"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

// kvCache is the L2 tier: a metadata.Cache backed by the shared
// clustarr-metadata-cache KV bucket, so a restarted gateway (there is only
// ever one replica, ADR-0007) does not lose every cached answer.
type kvCache struct {
	kv    events.KV
	clock clockwork.Clock
}

func newKVCache(kv events.KV, clock clockwork.Clock) *kvCache {
	return &kvCache{kv: kv, clock: clock}
}

func (c *kvCache) Get(ctx context.Context, key string, out any) (bool, error) {
	entry, err := c.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, events.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("metadata: get cache key %q: %w", key, err)
	}
	var e kvEntry
	if err := json.Unmarshal(entry.Value, &e); err != nil {
		return false, fmt.Errorf("metadata: decode cache entry %q: %w", key, err)
	}
	if !c.clock.Now().Before(e.ExpiresAt) {
		return false, nil
	}
	if err := json.Unmarshal(e.Value, out); err != nil {
		return false, fmt.Errorf("metadata: decode cached value %q: %w", key, err)
	}
	return true, nil
}

func (c *kvCache) Set(ctx context.Context, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("metadata: encode %q for cache: %w", key, err)
	}
	data, err := json.Marshal(kvEntry{Value: raw, ExpiresAt: c.clock.Now().Add(ttl)})
	if err != nil {
		return fmt.Errorf("metadata: encode cache entry %q: %w", key, err)
	}
	_, err = c.kv.Put(ctx, key, data)
	return err
}

var _ pkgmetadata.Cache = (*kvCache)(nil)
