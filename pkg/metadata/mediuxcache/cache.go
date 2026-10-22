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

// Package mediuxcache reads MediUX through the clustarr-mediux KV bucket
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md
// "Caching"), so a library is matched with as few requests as the sets'
// churn needs:
//
//   - a TheTVDB-to-TMDB mapping is kept for good: an id does not change;
//   - a show's, film's or collection's set list is kept SetTTL (a week),
//     and "no sets" NegativeTTL (a day), so an item MediUX has nothing for
//     is asked once a day at most;
//   - MediUX's content list -- every TMDB id that has a set -- is kept
//     ContentTTL (a day) and consulted before any set query: an item not
//     in it gets a negative entry without a request.
//
// The agent is the bucket's only writer; the cache is read-through, and
// an entry's age is judged from its fetchedAt, so the bucket's own TTL
// only bounds its size.
package mediuxcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
)

// Defaults.
const (
	DefaultSetTTL      = 7 * 24 * time.Hour
	DefaultNegativeTTL = 24 * time.Hour
	DefaultContentTTL  = 24 * time.Hour
)

// Lookup is what the cache asks MediUX: the client's methods.
type Lookup interface {
	ShowByTVDB(ctx context.Context, tvdbID string) (string, error)
	ShowSets(ctx context.Context, tmdbID string) (*mediux.Show, error)
	MovieSets(ctx context.Context, tmdbID string) (*mediux.Movie, error)
	CollectionSets(ctx context.Context, tmdbID string) (*mediux.Collection, error)
	ContentIDs(ctx context.Context) (*mediux.ContentIDs, error)
}

// Cache reads MediUX through the bucket.
type Cache struct {
	KV     events.KV
	Lookup Lookup
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// SetTTL, NegativeTTL and ContentTTL override the defaults when set.
	SetTTL, NegativeTTL, ContentTTL time.Duration
}

// Keys. Every one goes through events.KVKeyToken, which the KV grammar
// requires; an id is digits, so they come out as written.
func keyShowID(tvdb string) string     { return events.KVKeyToken("ids/show/" + tvdb) }
func keyShowSets(tmdb string) string   { return events.KVKeyToken("sets/show/" + tmdb) }
func keyMovieSets(tmdb string) string  { return events.KVKeyToken("sets/movie/" + tmdb) }
func keyCollection(tmdb string) string { return events.KVKeyToken("sets/collection/" + tmdb) }

const keyContent = "content/all"

type idEntry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	TMDBID    string    `json:"tmdbID,omitempty"`
	NotFound  bool      `json:"notFound,omitempty"`
}

type showEntry struct {
	FetchedAt time.Time    `json:"fetchedAt"`
	NotFound  bool         `json:"notFound,omitempty"`
	Show      *mediux.Show `json:"show,omitempty"`
}

type movieEntry struct {
	FetchedAt time.Time     `json:"fetchedAt"`
	NotFound  bool          `json:"notFound,omitempty"`
	Movie     *mediux.Movie `json:"movie,omitempty"`
}

type collectionEntry struct {
	FetchedAt  time.Time          `json:"fetchedAt"`
	NotFound   bool               `json:"notFound,omitempty"`
	Collection *mediux.Collection `json:"collection,omitempty"`
}

type contentEntry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Movies    []string  `json:"movies"`
	Shows     []string  `json:"shows"`
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) setTTL() time.Duration {
	if c.SetTTL > 0 {
		return c.SetTTL
	}
	return DefaultSetTTL
}

func (c *Cache) negativeTTL() time.Duration {
	if c.NegativeTTL > 0 {
		return c.NegativeTTL
	}
	return DefaultNegativeTTL
}

func (c *Cache) contentTTL() time.Duration {
	if c.ContentTTL > 0 {
		return c.ContentTTL
	}
	return DefaultContentTTL
}

// read decodes key into out, reporting whether it was there.
func (c *Cache) read(ctx context.Context, key string, out any) (bool, error) {
	e, err := c.KV.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mediuxcache: read %s: %w", key, err)
	}
	if err := json.Unmarshal(e.Value, out); err != nil {
		// A stale shape is a miss, not a failure: it is refetched.
		return false, nil
	}
	return true, nil
}

func (c *Cache) write(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mediuxcache: encode %s: %w", key, err)
	}
	if _, err := c.KV.Put(ctx, key, b); err != nil {
		return fmt.Errorf("mediuxcache: write %s: %w", key, err)
	}
	return nil
}

// fresh is whether an entry fetched at t, found (or not), is still good.
func (c *Cache) fresh(t time.Time, notFound bool) bool {
	ttl := c.setTTL()
	if notFound {
		ttl = c.negativeTTL()
	}
	return !t.IsZero() && c.now().Sub(t) < ttl
}

// ShowTMDB is the TMDB id of the show TheTVDB numbers tvdbID, from the
// mapping kept for good, or ErrNotFound (kept NegativeTTL).
func (c *Cache) ShowTMDB(ctx context.Context, tvdbID string) (string, error) {
	var e idEntry
	ok, err := c.read(ctx, keyShowID(tvdbID), &e)
	if err != nil {
		return "", err
	}
	if ok && e.TMDBID != "" {
		return e.TMDBID, nil
	}
	if ok && e.NotFound && c.fresh(e.FetchedAt, true) {
		return "", fmt.Errorf("mediuxcache: no show for tvdb %s (cached): %w", tvdbID, metadata.ErrNotFound)
	}
	id, err := c.Lookup.ShowByTVDB(ctx, tvdbID)
	switch {
	case errors.Is(err, metadata.ErrNotFound):
		if werr := c.write(ctx, keyShowID(tvdbID), idEntry{FetchedAt: c.now(), NotFound: true}); werr != nil {
			return "", werr
		}
		return "", err
	case err != nil:
		return "", err
	}
	if err := c.write(ctx, keyShowID(tvdbID), idEntry{FetchedAt: c.now(), TMDBID: id}); err != nil {
		return "", err
	}
	return id, nil
}

// Content is MediUX's content list, kept ContentTTL. nil, nil when it
// cannot be fetched and nothing is cached: the callers then ask directly.
func (c *Cache) Content(ctx context.Context) (*mediux.ContentIDs, error) {
	var e contentEntry
	ok, err := c.read(ctx, keyContent, &e)
	if err != nil {
		return nil, err
	}
	if ok && c.now().Sub(e.FetchedAt) < c.contentTTL() {
		return toContent(e), nil
	}
	ids, err := c.Lookup.ContentIDs(ctx)
	if err != nil {
		if ok {
			return toContent(e), nil // the stale list over none
		}
		return nil, err
	}
	e = contentEntry{FetchedAt: c.now(), Movies: keys(ids.Movies), Shows: keys(ids.Shows)}
	if err := c.write(ctx, keyContent, e); err != nil {
		return nil, err
	}
	return ids, nil
}

// has is whether the content list, when one can be had, names tmdbID of
// kind; true when no list can be had, so a lookup still goes out.
func (c *Cache) has(ctx context.Context, kind, tmdbID string) bool {
	ids, err := c.Content(ctx)
	if err != nil || ids == nil {
		return true
	}
	if kind == "show" {
		return ids.Shows[tmdbID]
	}
	return ids.Movies[tmdbID]
}

// ShowSets is the show tmdbID with its sets, from the bucket while fresh,
// else from MediUX -- unless the content list says MediUX has nothing for
// it, which is ErrNotFound without a query.
func (c *Cache) ShowSets(ctx context.Context, tmdbID string) (*mediux.Show, error) {
	var e showEntry
	ok, err := c.read(ctx, keyShowSets(tmdbID), &e)
	if err != nil {
		return nil, err
	}
	if ok && c.fresh(e.FetchedAt, e.NotFound) {
		if e.NotFound {
			return nil, fmt.Errorf("mediuxcache: no show %s (cached): %w", tmdbID, metadata.ErrNotFound)
		}
		return e.Show, nil
	}
	if !c.has(ctx, "show", tmdbID) {
		if err := c.write(ctx, keyShowSets(tmdbID), showEntry{FetchedAt: c.now(), NotFound: true}); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("mediuxcache: no show %s (not in the content list): %w", tmdbID, metadata.ErrNotFound)
	}
	show, err := c.Lookup.ShowSets(ctx, tmdbID)
	switch {
	case errors.Is(err, metadata.ErrNotFound):
		if werr := c.write(ctx, keyShowSets(tmdbID), showEntry{FetchedAt: c.now(), NotFound: true}); werr != nil {
			return nil, werr
		}
		return nil, err
	case err != nil:
		if ok && e.Show != nil {
			return e.Show, nil // the stale answer over none
		}
		return nil, err
	}
	if err := c.write(ctx, keyShowSets(tmdbID), showEntry{FetchedAt: c.now(), Show: show}); err != nil {
		return nil, err
	}
	return show, nil
}

// MovieSets is ShowSets for a film.
func (c *Cache) MovieSets(ctx context.Context, tmdbID string) (*mediux.Movie, error) {
	var e movieEntry
	ok, err := c.read(ctx, keyMovieSets(tmdbID), &e)
	if err != nil {
		return nil, err
	}
	if ok && c.fresh(e.FetchedAt, e.NotFound) {
		if e.NotFound {
			return nil, fmt.Errorf("mediuxcache: no movie %s (cached): %w", tmdbID, metadata.ErrNotFound)
		}
		return e.Movie, nil
	}
	if !c.has(ctx, "movie", tmdbID) {
		if err := c.write(ctx, keyMovieSets(tmdbID), movieEntry{FetchedAt: c.now(), NotFound: true}); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("mediuxcache: no movie %s (not in the content list): %w", tmdbID, metadata.ErrNotFound)
	}
	m, err := c.Lookup.MovieSets(ctx, tmdbID)
	switch {
	case errors.Is(err, metadata.ErrNotFound):
		if werr := c.write(ctx, keyMovieSets(tmdbID), movieEntry{FetchedAt: c.now(), NotFound: true}); werr != nil {
			return nil, werr
		}
		return nil, err
	case err != nil:
		if ok && e.Movie != nil {
			return e.Movie, nil
		}
		return nil, err
	}
	if err := c.write(ctx, keyMovieSets(tmdbID), movieEntry{FetchedAt: c.now(), Movie: m}); err != nil {
		return nil, err
	}
	return m, nil
}

// CollectionSets is ShowSets for a TMDB collection; the content list does
// not cover collections, so a miss always asks.
func (c *Cache) CollectionSets(ctx context.Context, tmdbID string) (*mediux.Collection, error) {
	var e collectionEntry
	ok, err := c.read(ctx, keyCollection(tmdbID), &e)
	if err != nil {
		return nil, err
	}
	if ok && c.fresh(e.FetchedAt, e.NotFound) {
		if e.NotFound {
			return nil, fmt.Errorf("mediuxcache: no collection %s (cached): %w", tmdbID, metadata.ErrNotFound)
		}
		return e.Collection, nil
	}
	col, err := c.Lookup.CollectionSets(ctx, tmdbID)
	switch {
	case errors.Is(err, metadata.ErrNotFound):
		if werr := c.write(ctx, keyCollection(tmdbID), collectionEntry{FetchedAt: c.now(), NotFound: true}); werr != nil {
			return nil, werr
		}
		return nil, err
	case err != nil:
		if ok && e.Collection != nil {
			return e.Collection, nil
		}
		return nil, err
	}
	if err := c.write(ctx, keyCollection(tmdbID), collectionEntry{FetchedAt: c.now(), Collection: col}); err != nil {
		return nil, err
	}
	return col, nil
}

func toContent(e contentEntry) *mediux.ContentIDs {
	ids := &mediux.ContentIDs{Movies: map[string]bool{}, Shows: map[string]bool{}}
	for _, id := range e.Movies {
		ids.Movies[id] = true
	}
	for _, id := range e.Shows {
		ids.Shows[id] = true
	}
	return ids
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
