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

package extmetrics

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/mediactl/clustarr/pkg/events"
)

// StateCache is the one read path from the External Metrics API and
// QueueGauge to the broker (split §9.4 as amended 2026-10-07): a 5 s cache per
// (stream, consumer) behind a singleflight, so a burst of aggregator retries,
// or several HPAs asking at once, makes one CONSUMER.INFO, and the leader's
// gauge and API never double-poll.
type StateCache struct {
	States  ConsumerStater
	TTL     time.Duration // 0 means 5 s
	Timeout time.Duration // 0 means 5 s
	Now     func() time.Time

	group singleflight.Group
	mu    sync.Mutex
	cache map[Series]cached
}

type cached struct {
	state events.ConsumerState
	at    time.Time
}

// NewStateCache is a StateCache over states with the default TTL and timeout.
func NewStateCache(states ConsumerStater) *StateCache { return &StateCache{States: states} }

// Get returns s's state: the cached read while it is younger than the TTL,
// else one broker read shared by every concurrent caller of s. A failed read
// is not cached.
func (c *StateCache) Get(ctx context.Context, s Series) (events.ConsumerState, error) {
	c.mu.Lock()
	if v, ok := c.cache[s]; ok && c.now().Sub(v.at) < c.ttl() {
		c.mu.Unlock()
		return v.state, nil
	}
	c.mu.Unlock()
	v, err, _ := c.group.Do(s.Stream+"\x00"+s.Consumer, func() (any, error) {
		// Its own context: one caller giving up must not fail the read the
		// others share. The timeout bounds it.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout())
		defer cancel()
		st, err := c.States.ConsumerState(rctx, s.Stream, s.Consumer)
		if err != nil {
			return events.ConsumerState{}, err
		}
		c.mu.Lock()
		if c.cache == nil {
			c.cache = map[Series]cached{}
		}
		c.cache[s] = cached{state: st, at: c.now()}
		c.mu.Unlock()
		return st, nil
	})
	if err != nil {
		return events.ConsumerState{}, err
	}
	return v.(events.ConsumerState), nil
}

// ConsumerState is Get for one (stream, durable): the cache is itself a
// ConsumerStater, so the manager's dispatch ledger shares its reads.
func (c *StateCache) ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error) {
	return c.Get(ctx, Series{Stream: stream, Consumer: durable})
}

func (c *StateCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *StateCache) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return defaultTTL
}

func (c *StateCache) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}
