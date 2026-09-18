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
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/jonboulle/clockwork"
)

// LRUCache is the in-process L1 cache: bounded by entry count, JSON-encoded
// so it behaves exactly like a remote L2 would, and carrying its own expiry
// per entry (hashicorp/golang-lru/v2's expirable variant has one TTL for the
// whole cache, which cannot hold both a 1-hour search-result entry and a
// 90-day crosswalk entry at once -- this wrapper stores expiresAt itself
// instead).
type LRUCache struct {
	mu    sync.Mutex
	cache *lru.Cache[string, cacheEntry]
	clock clockwork.Clock
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// NewLRUCache builds an LRUCache holding at most size entries. Production
// callers pass clockwork.NewRealClock(); tests pass clockwork.NewFakeClock()
// so TTL expiry is exercised without a real sleep.
func NewLRUCache(size int, clock clockwork.Clock) (*LRUCache, error) {
	c, err := lru.New[string, cacheEntry](size)
	if err != nil {
		return nil, fmt.Errorf("metadata: new LRU cache: %w", err)
	}
	return &LRUCache{cache: c, clock: clock}, nil
}

func (c *LRUCache) Get(_ context.Context, key string, out any) (bool, error) {
	c.mu.Lock()
	entry, ok := c.cache.Get(key)
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	if !c.clock.Now().Before(entry.expiresAt) {
		c.mu.Lock()
		c.cache.Remove(key)
		c.mu.Unlock()
		return false, nil
	}
	if err := json.Unmarshal(entry.data, out); err != nil {
		return false, fmt.Errorf("metadata: decode cached %q: %w", key, err)
	}
	return true, nil
}

func (c *LRUCache) Set(_ context.Context, key string, v any, ttl time.Duration) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("metadata: encode %q for cache: %w", key, err)
	}
	c.mu.Lock()
	c.cache.Add(key, cacheEntry{data: data, expiresAt: c.clock.Now().Add(ttl)})
	c.mu.Unlock()
	return nil
}

var _ Cache = (*LRUCache)(nil)
