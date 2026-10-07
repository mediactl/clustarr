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

package ui

import (
	"container/list"
	"sync"
)

// artCache is /art's byte cache (artwork design §B.8 as amended
// 2026-10-07): a least-recently-used set of whole objects keyed by digest,
// bounded by bytes. A digest names its bytes, so an entry is never stale and
// nothing is ever invalidated. An object larger than a quarter of the budget
// is served but not kept, so one large image cannot flush the posters a page
// shows. Its methods are get and add, never Put or Update: TestUINeverWrites
// bans those names under ui/ whatever the receiver.
type artCache struct {
	mu     sync.Mutex
	budget int64
	used   int64
	order  *list.List // front is most recent; values are *artCacheEntry
	byKey  map[string]*list.Element
}

type artCacheEntry struct {
	digest string
	body   []byte
}

// newArtCache returns a cache of budget bytes; zero or less keeps nothing.
func newArtCache(budget int64) *artCache {
	return &artCache{budget: budget, order: list.New(), byKey: map[string]*list.Element{}}
}

// get returns digest's bytes and refreshes its recency. The slice is shared:
// callers only write it to a response.
func (c *artCache) get(digest string) ([]byte, bool) {
	if c == nil || digest == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[digest]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*artCacheEntry).body, true
}

// add keeps b under digest, evicting the least recently used entries until
// it fits. An entry over a quarter of the budget, or any entry under a zero
// budget, is not kept.
func (c *artCache) add(digest string, b []byte) {
	if c == nil || digest == "" || c.budget <= 0 || int64(len(b)) > c.budget/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[digest]; ok {
		c.order.MoveToFront(el)
		return
	}
	for c.used+int64(len(b)) > c.budget {
		back := c.order.Back()
		if back == nil {
			break
		}
		old := back.Value.(*artCacheEntry)
		c.order.Remove(back)
		delete(c.byKey, old.digest)
		c.used -= int64(len(old.body))
	}
	c.byKey[digest] = c.order.PushFront(&artCacheEntry{digest: digest, body: b})
	c.used += int64(len(b))
}
