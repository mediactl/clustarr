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

package tuner

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// SearchFunc asks the metadata gateway's search (events.RPCMetadataSearch,
// the Add New search) for a title's movies or series.
type SearchFunc func(ctx context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error)

// Programme artwork (owner, 2026-10-27): the provider's guide carries no
// programme images, so the guide's titles are looked up as movies and
// series through the metadata gateway and a poster found becomes the
// programme's <icon>, which Plex shows in its guide. A lookup never holds a
// guide build: the build takes what the cache has, unknown titles are
// queued, a background pass resolves them a few a second, and the guide is
// rebuilt once a pass found something. The cache is an object in the Live
// TV store: a hit is good for artHitTTL, a miss for artMissTTL.
const (
	artHitTTL         = 30 * 24 * time.Hour
	artMissTTL        = 7 * 24 * time.Hour
	artLookupsPerPass = 300
	artLookupInterval = 400 * time.Millisecond
	artLookupTimeout  = 15 * time.Second
	artQueueMax       = 4 * artLookupsPerPass
)

// ArtObject is the clustarr-livetv object caching a provider's programme
// artwork lookups.
func ArtObject(key types.NamespacedName) string {
	return "programme-art/" + key.Namespace + "/" + key.Name
}

type artEntry struct {
	URL  string    `json:"url,omitempty"`
	Kind string    `json:"kind,omitempty"`
	At   time.Time `json:"at"`
}

type artCache struct {
	Entries map[string]artEntry `json:"entries"`
}

// programmeArt is one provider's programme artwork: the cache, and the
// titles waiting for a lookup.
type programmeArt struct {
	mu      sync.Mutex
	entries map[string]artEntry
	pending map[string]xmltv.ArtQuery
	running bool
}

func artKey(q xmltv.ArtQuery) string { return release.TitleNorm(q.Title) + "|" + strconv.Itoa(q.Year) }

// resolve is the Enricher's ArtResolver: the cached answer, or none while
// the title waits for a lookup.
func (a *programmeArt) resolve(now time.Time, q xmltv.ArtQuery) (xmltv.Art, bool) {
	k := artKey(q)
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.entries[k]; ok {
		ttl := artMissTTL
		if e.URL != "" {
			ttl = artHitTTL
		}
		if now.Sub(e.At) < ttl {
			if e.URL == "" {
				return xmltv.Art{}, false
			}
			return xmltv.Art{URL: e.URL, Kind: xmltv.ArtKind(e.Kind)}, true
		}
	}
	if a.pending == nil {
		a.pending = map[string]xmltv.ArtQuery{}
	}
	if _, queued := a.pending[k]; !queued && len(a.pending) < artQueueMax {
		a.pending[k] = q
	}
	return xmltv.Art{}, false
}

// take removes up to n pending queries for a pass, marking it running; nil
// when none wait or a pass runs already.
func (a *programmeArt) take(n int) map[string]xmltv.ArtQuery {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || len(a.pending) == 0 {
		return nil
	}
	out := make(map[string]xmltv.ArtQuery, min(n, len(a.pending)))
	for k, q := range a.pending {
		if len(out) == n {
			break
		}
		out[k] = q
		delete(a.pending, k)
	}
	a.running = true
	return out
}

// record stores a lookup's answer.
func (a *programmeArt) record(k string, e artEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = map[string]artEntry{}
	}
	a.entries[k] = e
}

// snapshot is a copy of the cache for the store.
func (a *programmeArt) snapshot() artCache {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := artCache{Entries: make(map[string]artEntry, len(a.entries))}
	for k, e := range a.entries {
		c.Entries[k] = e
	}
	return c
}

// artFor is p's artwork, loaded from the store the first time; p.mu is
// held.
func (t *Tuner) artFor(p *provider) *programmeArt {
	if p.art == nil {
		p.art = &programmeArt{entries: map[string]artEntry{}}
		var c artCache
		if t.readJSON(ArtObject(p.key), &c) && c.Entries != nil {
			p.art.entries = c.Entries
		}
	}
	return p.art
}

// enricher is the guide builder's Enricher for p: its programme artwork
// when the tuner can search, the classification alone otherwise; p.mu is
// held.
func (t *Tuner) enricher(p *provider) *xmltv.Enricher {
	e := &xmltv.Enricher{}
	if t.o.Search != nil {
		art := t.artFor(p)
		e.Art = func(q xmltv.ArtQuery) (xmltv.Art, bool) { return art.resolve(t.o.Now(), q) }
	}
	return e
}

// startArtLookups runs a lookup pass for p's waiting titles, unless one
// runs already or the tuner cannot search.
func (t *Tuner) startArtLookups(p *provider) {
	if t.o.Search == nil {
		return
	}
	p.mu.Lock()
	art := t.artFor(p)
	p.mu.Unlock()
	batch := art.take(artLookupsPerPass)
	if batch == nil {
		return
	}
	go t.lookupArt(p, art, batch)
}

// lookupArt resolves one pass of titles, stores the cache, and has the
// guide rebuilt when anything was found; a pass that leaves titles waiting
// starts the next.
func (t *Tuner) lookupArt(p *provider, art *programmeArt, batch map[string]xmltv.ArtQuery) {
	log := logging.FromContext(t.base)
	for {
		found, failed := 0, 0
		for k, q := range batch {
			if t.base.Err() != nil {
				return
			}
			e, err := t.searchArt(q)
			if err != nil {
				failed++
				if failed > 10 {
					log.Warn("livetv: programme artwork lookups failing; the rest wait for the next pass", "provider", label(p.key), "error", err)
					break
				}
				continue // left for the next render to queue again
			}
			if e.URL != "" {
				found++
			}
			art.record(k, e)
			select {
			case <-t.base.Done():
				return
			case <-time.After(artLookupInterval):
			}
		}
		t.putJSON(t.base, ArtObject(p.key), art.snapshot())
		art.mu.Lock()
		art.running = false
		art.mu.Unlock()
		if found > 0 {
			t.scheduleRender(p)
		}
		if failed > 10 {
			return
		}
		if batch = art.take(artLookupsPerPass); batch == nil {
			return
		}
	}
}

// searchArt asks the gateway for q's title as a series and as a movie, a
// movie first on a movie channel or for a film-length programme, and takes
// the first hit whose title is q's (and whose year is within one of q's,
// when q has one). No hit is a miss, cached as such.
func (t *Tuner) searchArt(q xmltv.ArtQuery) (artEntry, error) {
	now := t.o.Now()
	kinds := []commonv1.MediaKind{commonv1.MediaKindSeries, commonv1.MediaKindMovie}
	if strings.Contains(strings.ToLower(q.Channel.Group), "movie") || q.Duration >= 100*time.Minute {
		kinds[0], kinds[1] = kinds[1], kinds[0]
	}
	want := release.TitleNorm(q.Title)
	for _, kind := range kinds {
		ctx, cancel := context.WithTimeout(t.base, artLookupTimeout)
		resp, err := t.o.Search(ctx, schema.MetadataRequest{Kind: kind, Text: q.Title, Year: int32(q.Year)})
		cancel()
		if err != nil {
			return artEntry{}, err
		}
		for _, raw := range resp.Results {
			var h struct {
				Title  string `json:"title"`
				Year   int32  `json:"year"`
				Poster string `json:"poster"`
			}
			if json.Unmarshal(raw, &h) != nil || h.Poster == "" || release.TitleNorm(h.Title) != want {
				continue
			}
			if q.Year != 0 && h.Year != 0 && (int(h.Year)-q.Year > 1 || q.Year-int(h.Year) > 1) {
				continue
			}
			e := artEntry{URL: h.Poster, Kind: string(xmltv.ArtSeries), At: now}
			if kind == commonv1.MediaKindMovie {
				e.Kind = string(xmltv.ArtMovie)
			}
			return e, nil
		}
	}
	return artEntry{At: now}, nil
}
