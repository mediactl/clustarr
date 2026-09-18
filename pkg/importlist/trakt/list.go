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

package trakt

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrUnsupportedKind is returned by New when kind is not movie or series;
// Trakt has no other kind of list.
var ErrUnsupportedKind = errors.New("importlist/trakt: kind must be movie or series")

// List fetches one Trakt watchlist, collection, user list, trending or
// popular feed, for a single catalog kind.
type List struct {
	name  string
	kind  commonv1.MediaKind
	cfg   importlist.TraktConfig
	creds Credentials
	store importlist.TokenStore
	flow  *DeviceFlow
	opts  options
}

// New returns a List. flow may be nil, in which case a DeviceFlow is built
// from creds and opts. It returns ErrUnsupportedKind unless kind is movie
// or series.
func New(name string, kind commonv1.MediaKind, cfg importlist.TraktConfig, creds Credentials, store importlist.TokenStore, flow *DeviceFlow, opts ...Option) (*List, error) {
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindSeries {
		return nil, ErrUnsupportedKind
	}
	o := newOptions(opts)
	if flow == nil {
		flow = NewDeviceFlow(creds, WithBaseURL(o.baseURL), WithHTTPClient(o.client))
	}
	return &List{name: name, kind: kind, cfg: cfg, creds: creds, store: store, flow: flow, opts: o}, nil
}

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList.
func (l *List) Kind() commonv1.MediaKind { return l.kind }

// path builds the Trakt API path for l's configured list type and kind.
func (l *List) path() (string, error) {
	seg := "movies"
	if l.kind == commonv1.MediaKindSeries {
		seg = "shows"
	}
	switch l.cfg.ListType {
	case importlist.TraktListTypeWatchlist:
		return fmt.Sprintf("/users/%s/watchlist/%s", l.cfg.Username, seg), nil
	case importlist.TraktListTypeCollection:
		return fmt.Sprintf("/users/%s/collection/%s", l.cfg.Username, seg), nil
	case importlist.TraktListTypeList:
		return fmt.Sprintf("/users/%s/lists/%s/items/%s", l.cfg.Username, l.cfg.ListSlug, seg), nil
	case importlist.TraktListTypeTrending:
		return "/" + seg + "/trending", nil
	case importlist.TraktListTypePopular:
		return "/" + seg + "/popular", nil
	default:
		return "", fmt.Errorf("importlist/trakt: unknown list type %q", l.cfg.ListType)
	}
}

func (l *List) doFetch(ctx context.Context, accessToken string) (*http.Response, []traktEntry, []byte, error) {
	p, err := l.path()
	if err != nil {
		return nil, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.opts.baseURL+p+"?extended=full", nil)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", l.creds.ClientID)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	var out []traktEntry
	resp, body, err := doJSON(ctx, l.opts.client, req, &out)
	return resp, out, body, err
}

// Fetch implements importlist.ImportList. It refreshes the stored token and
// retries exactly once when the first request comes back 401.
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.trakt.fetch")
	defer span.End()

	tok, _, err := l.store.Load(ctx)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	resp, entries, body, err := l.doFetch(ctx, tok.AccessToken)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && tok.RefreshToken != "" {
		refreshed, err := l.flow.Refresh(ctx, tok.RefreshToken)
		if err != nil {
			err = fmt.Errorf("importlist/trakt: refresh after 401: %w", err)
			tracing.RecordError(span, err)
			return nil, err
		}
		if err := l.store.Save(ctx, refreshed); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		resp, entries, body, err = l.doFetch(ctx, refreshed.AccessToken)
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		err := &APIError{StatusCode: resp.StatusCode, Body: string(body)}
		tracing.RecordError(span, err)
		return nil, err
	}

	items := make([]importlist.Item, 0, len(entries))
	for _, e := range entries {
		m := e.Movie
		if l.kind == commonv1.MediaKindSeries {
			m = e.Show
		}
		if m == nil {
			continue
		}
		items = append(items, importlist.Item{
			Title: m.Title,
			Year:  m.Year,
			ExternalIDs: importlist.ExternalIDs{
				IMDb: m.IDs.Imdb,
				TMDB: importlist.NonZeroString(m.IDs.Tmdb),
				TVDB: importlist.NonZeroString(m.IDs.Tvdb),
			},
		})
	}
	return items, nil
}

type traktIDs struct {
	Imdb string `json:"imdb"`
	Tmdb int    `json:"tmdb"`
	Tvdb int    `json:"tvdb"`
}

type traktMovie struct {
	Title string   `json:"title"`
	Year  int32    `json:"year"`
	IDs   traktIDs `json:"ids"`
}

type traktEntry struct {
	Movie *traktMovie `json:"movie"`
	Show  *traktMovie `json:"show"`
}
