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

package scenemap

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Fetcher is the upstream Cached reads through; *XEM is the production
// one.
type Fetcher interface {
	HaveMap(ctx context.Context) ([]int64, error)
	Mappings(ctx context.Context, tvdbID int64) ([]Mapping, error)
	Names(ctx context.Context) (map[int64][]SceneName, error)
}

// Default TTLs. Sonarr refreshes its TheXEM series list when it is three
// hours old (XemService.Handle: IsExpired(TimeSpan.FromHours(3))) and its
// scene names on the same three-hour scene-mapping schedule; a series' own
// rows it re-reads on every series refresh, which is twelve-hourly by
// default. These match.
const (
	DefaultHaveMapTTL = 3 * time.Hour
	DefaultNamesTTL   = 3 * time.Hour
	DefaultMappingTTL = 12 * time.Hour
)

// Options tunes Cached; a zero field takes its default.
type Options struct {
	HaveMapTTL time.Duration
	NamesTTL   time.Duration
	MappingTTL time.Duration
}

// Cached is a Source over a Fetcher and a metadata.Cache -- the gateway's
// tiered L1/L2 cache, or a metadata.LRUCache on its own.
//
// It asks for a series' rows only when havemap lists the series, as Sonarr
// does, so an unmapped series -- almost every series -- costs no request
// of its own. The scene-names table covers every series in one response
// and is cached whole. Concurrent callers wanting the same entry share
// one request. A failed request is not cached; a series TheXEM answers
// "no show" for is, as an empty table, for the rows' TTL.
type Cached struct {
	src   Fetcher
	cache metadata.Cache
	opts  Options

	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done chan struct{}
	err  error
}

// NewCached builds a Cached Source.
func NewCached(src Fetcher, cache metadata.Cache, opts Options) *Cached {
	if opts.HaveMapTTL <= 0 {
		opts.HaveMapTTL = DefaultHaveMapTTL
	}
	if opts.NamesTTL <= 0 {
		opts.NamesTTL = DefaultNamesTTL
	}
	if opts.MappingTTL <= 0 {
		opts.MappingTTL = DefaultMappingTTL
	}
	return &Cached{src: src, cache: cache, opts: opts, flights: map[string]*flight{}}
}

// Cache keys. Every segment is a constant or a decimal id, and the id goes
// through events.KVKeyToken regardless, so a key is always one a NATS KV
// bucket accepts (CLAUDE.md; scenemap's contract test puts each shape to a
// real embedded server).
const (
	keyHaveMap = "scenemap.thexem.havemap"
	keyNames   = "scenemap.thexem.names"
)

func keyMappings(tvdbID int64) string {
	return "scenemap.thexem.tvdb." + events.KVKeyToken(strconv.FormatInt(tvdbID, 10))
}

// SceneMap returns TheXEM's table and scene names for one TVDB series. An
// unmapped series is an empty Map, not an error; an error means TheXEM
// could not be asked, and the caller should proceed without scene
// numbering rather than treat the series as unmapped.
func (c *Cached) SceneMap(ctx context.Context, tvdbID int64) (*Map, error) {
	ctx, span := tracing.Start(ctx, "scenemap.Cached.SceneMap")
	defer span.End()

	m := &Map{TVDBID: tvdbID}

	names, err := load(ctx, c, keyNames, c.opts.NamesTTL, c.src.Names)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	m.Names = names[tvdbID]

	have, err := load(ctx, c, keyHaveMap, c.opts.HaveMapTTL, c.src.HaveMap)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if !slices.Contains(have, tvdbID) {
		return m, nil
	}

	rows, err := load(ctx, c, keyMappings(tvdbID), c.opts.MappingTTL, func(ctx context.Context) ([]Mapping, error) {
		rows, err := c.src.Mappings(ctx, tvdbID)
		if err == nil && rows == nil {
			rows = []Mapping{} // "no rows" is an answer to cache, not a miss
		}
		return rows, err
	})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	m.Mappings = rows
	return m, nil
}

// load returns key's value from the cache or, on a miss, from fetch,
// storing what fetch returns for ttl. Concurrent loads of one key share
// one fetch: the first caller fetches and stores, the rest wait and then
// read what it stored. If that store failed they miss again, and the next
// one fetches for itself.
func load[T any](ctx context.Context, c *Cached, key string, ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	logger := logging.FromContext(ctx)
	var zero T
	for {
		var v T
		hit, err := c.cache.Get(ctx, key, &v)
		if err != nil {
			// A corrupt or unreadable entry is a miss, not an outage.
			logger.WarnContext(ctx, "scenemap: cache read failed; refetching", "key", key, "error", err)
		} else if hit {
			return v, nil
		}

		c.mu.Lock()
		if f, ok := c.flights[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return zero, ctx.Err()
			}
			if f.err != nil {
				// The leader's own deadline is not this caller's: fetch
				// again rather than inherit a cancellation.
				if ctx.Err() == nil && (errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded)) {
					continue
				}
				return zero, f.err
			}
			continue
		}
		f := &flight{done: make(chan struct{})}
		c.flights[key] = f
		c.mu.Unlock()

		v, err = fetch(ctx)
		if err == nil {
			if serr := c.cache.Set(ctx, key, v, ttl); serr != nil {
				logger.WarnContext(ctx, "scenemap: cache write failed", "key", key, "error", serr)
			}
		}
		f.err = err
		c.mu.Lock()
		delete(c.flights, key)
		c.mu.Unlock()
		close(f.done)
		if err != nil {
			return zero, err
		}
		return v, nil
	}
}

var _ Source = (*Cached)(nil)
