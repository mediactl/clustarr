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

package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
)

// PMS asks a custom provider for an item's extras on every refresh --
// GET {root}/library/metadata/{ratingKey}/extras, a route Plex's provider
// docs do not list -- and reads a 404 or an empty list as "this item has
// none", deleting the trailers it holds for it. Answering 404 cost the
// owner's library the Internet Video Archive trailers Plex had attached
// (kind-cluster-plex, 2026-10-06). The route answers Plex's own extras for
// the item's Plex id, as Plex's metadata service sends them: clips with
// their extraType and Media URLs, which PMS plays from Internet Video
// Archive. An item with no Plex id answers none; when Plex cannot be asked
// the answer is an error, never an empty list.

// DefaultPlexTVBaseURL is Plex's metadata service.
const DefaultPlexTVBaseURL = "https://metadata.provider.plex.tv"

const (
	// maxExtrasBody caps one extras answer; Arrival's 44 extras are 40 KiB.
	maxExtrasBody = 4 << 20
	// plexTVExtrasPage is how many extras one request asks Plex for.
	plexTVExtrasPage = 200
	// extrasTTL is how long an item's extras are reused: a library refresh
	// asks for every item, and Plex's extras change rarely.
	extrasTTL = 24 * time.Hour
	// extrasCacheMax bounds the cache; a library is a few thousand items.
	extrasCacheMax = 8192
	extrasTimeout  = 15 * time.Second
)

var (
	// ErrPlexTV is a request Plex's metadata service did not answer with
	// extras.
	ErrPlexTV = errors.New("plex: the metadata service did not answer")
	// ErrResponseTooLarge is an answer over maxExtrasBody.
	ErrResponseTooLarge = errors.New("plex: the metadata service's answer is too large")
)

// Extra is one of an item's extras exactly as Plex's metadata service sent
// it, so every field PMS reads reaches it unchanged.
type Extra = json.RawMessage

// PlexTVExtras fetches an item's extras from Plex's metadata service with
// the server's token, and caches them per Plex id for extrasTTL.
type PlexTVExtras struct {
	// BaseURL is the metadata service; empty is DefaultPlexTVBaseURL.
	BaseURL string
	// Token is the server's plex.tv token (PlexOnlineToken). It travels
	// only in the X-Plex-Token header of requests to BaseURL.
	Token string
	// HTTP is the client; nil is one with a 15 s timeout.
	HTTP *http.Client
	// Limiter paces requests to the metadata service; nil does not pace.
	Limiter *rate.Limiter
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cachedExtras
}

type cachedExtras struct {
	items []Extra
	at    time.Time
}

func (p *PlexTVExtras) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Extras returns the extras Plex holds for the item with Plex id plexID.
func (p *PlexTVExtras) Extras(ctx context.Context, plexID string) ([]Extra, error) {
	now := p.now()
	p.mu.Lock()
	if c, ok := p.cache[plexID]; ok && now.Sub(c.at) < extrasTTL {
		p.mu.Unlock()
		return c.items, nil
	}
	p.mu.Unlock()

	if p.Limiter != nil {
		if err := p.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	items, err := p.fetch(ctx, plexID)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[string]cachedExtras{}
	}
	if len(p.cache) >= extrasCacheMax {
		for id, c := range p.cache {
			if now.Sub(c.at) >= extrasTTL {
				delete(p.cache, id)
			}
		}
		if len(p.cache) >= extrasCacheMax {
			clear(p.cache)
		}
	}
	p.cache[plexID] = cachedExtras{items: items, at: now}
	return items, nil
}

func (p *PlexTVExtras) fetch(ctx context.Context, plexID string) ([]Extra, error) {
	base := p.BaseURL
	if base == "" {
		base = DefaultPlexTVBaseURL
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: extrasTimeout}
	}
	u := base + "/library/metadata/" + url.PathEscape(plexID) + "/extras?" + url.Values{
		"X-Plex-Container-Start": {"0"},
		"X-Plex-Container-Size":  {strconv.Itoa(plexTVExtrasPage)},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", p.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlexTV, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", ErrPlexTV, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxExtrasBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlexTV, err)
	}
	if len(body) > maxExtrasBody {
		return nil, ErrResponseTooLarge
	}
	var out struct {
		MediaContainer struct {
			Metadata []Extra `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlexTV, err)
	}
	if out.MediaContainer.Metadata == nil {
		return []Extra{}, nil
	}
	return out.MediaContainer.Metadata, nil
}

type extrasContainer struct {
	Offset     int     `json:"offset"`
	TotalSize  int     `json:"totalSize"`
	Identifier string  `json:"identifier"`
	Size       int     `json:"size"`
	Metadata   []Extra `json:"Metadata"`
}

type extrasContainerResponse struct {
	MediaContainer extrasContainer `json:"MediaContainer"`
}

func (h *handler) handleExtras(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		plexID, ok := extrasPlexID(root, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		items := []Extra{}
		if plexID != "" {
			if h.opts.Extras == nil {
				http.Error(w, "the provider has no Plex token to fetch extras with", http.StatusServiceUnavailable)
				return
			}
			got, err := h.opts.Extras(r.Context(), plexID)
			if err != nil {
				logging.FromContext(r.Context()).Error("plex: fetch extras", "plexID", plexID, "error", err)
				http.Error(w, "Plex's metadata service did not answer", http.StatusBadGateway)
				return
			}
			items = got
		}

		start, size := containerWindow(r, len(items))
		page := items[min(start, len(items)):min(start+size, len(items))]
		writeJSON(w, http.StatusOK, extrasContainerResponse{MediaContainer: extrasContainer{
			Offset:     start,
			TotalSize:  len(items),
			Identifier: root.identifier,
			Size:       len(page),
			Metadata:   page,
		}})
	}
}

// containerWindow is the X-Plex-Container-Start and -Size PMS asks for, as
// query parameters or headers; the whole list when it names none.
func containerWindow(r *http.Request, total int) (start, size int) {
	param := func(name string) (int, bool) {
		v := r.URL.Query().Get(name)
		if v == "" {
			v = r.Header.Get(name)
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	start, _ = param("X-Plex-Container-Start")
	size, ok := param("X-Plex-Container-Size")
	if !ok {
		size = total
	}
	return start, size
}

// extrasPlexID is the Plex id of the item ratingKey names, "" when it has
// none; false when the root does not serve it.
func extrasPlexID(root rootDef, idx *projection.Index, ratingKey string) (string, bool) {
	uid, season, isSeason, ok := resolveKey(idx, ratingKey)
	if !ok {
		return "", false
	}
	if isSeason {
		s, ok := idx.SeriesByUID(uid)
		if !ok || !root.declares(typeSeason) {
			return "", false
		}
		return seasonPlexID(s, season), true
	}
	if m, ok := idx.MovieByUID(uid); ok && root.declares(typeMovie) {
		return moviePlexID(m), true
	}
	if s, ok := idx.SeriesByUID(uid); ok && root.declares(typeShow) {
		return seriesPlexID(s), true
	}
	if e, ok := idx.EpisodeByUID(uid); ok && root.declares(typeEpisode) {
		return episodePlexID(e), true
	}
	return "", false
}
