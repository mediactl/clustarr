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
	"sort"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
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

// cacheKey builds an item-level cache key: <kind>.<sorted k=v external ids>.
// See this task's "Judgment calls" for why this deliberately drops the
// <provider> segment the spec's KV table literally shows.
//
// The separators are "." and "_" rather than the ":" and "," this originally
// used, because a NATS KV key must match ^[-/_=\.a-zA-Z0-9]+$ and both of
// those are illegal. Every L2 write and read failed with "nats: invalid key",
// so every metadata refresh -- Movie and Series alike -- naked and redelivered
// forever, status.metadata never landed, and no item ever became Ready. No
// unit or envtest suite could see it: they run against the in-memory bus,
// which has no key grammar. It took the first run on a real cluster.
//
// events.KVKeyToken defends the same invariant for the values, which are
// provider ids from outside this process and are not guaranteed to be
// alphanumeric. It used to be a kvKeyTok local to this file; task C14 lifted
// it into pkg/events, which had its own (non-)implementation of the same
// grammar and was getting it wrong for LeaseKey/PendingKey/ExclusionKey.
func cacheKey(kind commonv1.MediaKind, ids pkgmetadata.ExternalIDs) string {
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, events.KVKeyToken(k)+"="+events.KVKeyToken(ids[k]))
	}
	return events.KVKeyToken(string(kind)) + "." + strings.Join(parts, "_")
}
