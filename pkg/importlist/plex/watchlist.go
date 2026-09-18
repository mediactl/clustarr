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

// Package plex is a pkg/importlist provider for the authenticated user's
// Plex Discover watchlist.
package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// defaultBaseURL is Plex's Discover service, which serves the watchlist
// independently of any single Plex Media Server.
const defaultBaseURL = "https://discover.provider.plex.tv"

// defaultPageSize is how many items Fetch requests per page when
// WithPageSize is not given.
const defaultPageSize = 100

// ErrUnsupportedKind is returned by New when kind is not movie or series.
var ErrUnsupportedKind = errors.New("importlist/plex: kind must be movie or series")

// Option configures a Watchlist.
type Option func(*options)

type options struct {
	baseURL  string
	client   *http.Client
	pageSize int32
}

// WithBaseURL overrides the Plex Discover base URL, for tests.
func WithBaseURL(u string) Option { return func(o *options) { o.baseURL = u } }

// WithHTTPClient overrides the *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// WithPageSize overrides how many items are requested per page.
func WithPageSize(n int32) Option { return func(o *options) { o.pageSize = n } }

// Watchlist fetches the authenticated user's Plex Discover watchlist for a
// single catalog kind.
type Watchlist struct {
	name     string
	kind     commonv1.MediaKind
	token    string
	clientID string
	opts     options
}

// New returns a Watchlist. It returns ErrUnsupportedKind unless kind is
// movie or series -- the Plex watchlist has no other kind.
func New(name string, kind commonv1.MediaKind, token, clientID string, opts ...Option) (*Watchlist, error) {
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindSeries {
		return nil, ErrUnsupportedKind
	}
	o := options{baseURL: defaultBaseURL, client: http.DefaultClient, pageSize: defaultPageSize}
	for _, apply := range opts {
		apply(&o)
	}
	return &Watchlist{name: name, kind: kind, token: token, clientID: clientID, opts: o}, nil
}

// Name implements importlist.ImportList.
func (w *Watchlist) Name() string { return w.name }

// Kind implements importlist.ImportList.
func (w *Watchlist) Kind() commonv1.MediaKind { return w.kind }

// typeParam is Plex Discover's numeric metadata type filter: 1 is movie, 2
// is show.
func (w *Watchlist) typeParam() string {
	if w.kind == commonv1.MediaKindSeries {
		return "2"
	}
	return "1"
}

// Fetch implements importlist.ImportList. It pages through the watchlist,
// stopping once a page comes back shorter than the requested page size --
// the request-side pagination headers are verified against Plex's own
// docs, but no response "totalSize" field is, so page length is the only
// defensible stop condition.
func (w *Watchlist) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.plex.fetch")
	defer span.End()

	var items []importlist.Item
	start := int32(0)
	for {
		page, err := w.fetchPage(ctx, start)
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		items = append(items, page...)
		if int32(len(page)) < w.opts.pageSize {
			return items, nil
		}
		start += w.opts.pageSize
	}
}

func (w *Watchlist) fetchPage(ctx context.Context, start int32) ([]importlist.Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.opts.baseURL+"/library/sections/watchlist/all", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("includeFields", "title,type,year,ratingKey")
	q.Set("excludeElements", "Image")
	q.Set("includeGuids", "1")
	q.Set("sort", "watchlistedAt:desc")
	q.Set("type", w.typeParam())
	q.Set("X-Plex-Token", w.token)
	q.Set("X-Plex-Client-Identifier", w.clientID)
	q.Set("X-Plex-Container-Start", strconv.Itoa(int(start)))
	q.Set("X-Plex-Container-Size", strconv.Itoa(int(w.opts.pageSize)))
	req.URL.RawQuery = q.Encode()

	resp, err := w.opts.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("importlist/plex: unexpected status %d", resp.StatusCode)
	}

	var out struct {
		MediaContainer struct {
			Metadata []struct {
				Title string `json:"title"`
				Year  int32  `json:"year"`
				// GUID is the item's single primary identifier
				// (e.g. "plex://movie/..."), a sibling JSON field
				// distinct from the Guid array below; it is declared
				// here, with its own exact-case tag, only so it does
				// not collide with Guid under encoding/json's
				// case-insensitive fallback matching.
				GUID string `json:"guid"`
				Guid []struct {
					ID string `json:"id"`
				} `json:"Guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("importlist/plex: decode: %w", err)
	}

	items := make([]importlist.Item, 0, len(out.MediaContainer.Metadata))
	for _, m := range out.MediaContainer.Metadata {
		ids := importlist.ExternalIDs{}
		for _, g := range m.Guid {
			switch {
			case strings.HasPrefix(g.ID, "imdb://"):
				ids.IMDb = strings.TrimPrefix(g.ID, "imdb://")
			case strings.HasPrefix(g.ID, "tmdb://"):
				ids.TMDB = strings.TrimPrefix(g.ID, "tmdb://")
			case strings.HasPrefix(g.ID, "tvdb://"):
				ids.TVDB = strings.TrimPrefix(g.ID, "tvdb://")
			}
		}
		items = append(items, importlist.Item{Title: m.Title, Year: m.Year, ExternalIDs: ids})
	}
	return items, nil
}
