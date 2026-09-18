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

// Package mdblist is a pkg/importlist provider for lists hosted on
// mdblist.com.
package mdblist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrUnsupportedKind is returned by New when kind is not movie or series.
var ErrUnsupportedKind = errors.New("importlist/mdblist: kind must be movie or series")

// List fetches one mdblist.com list, filtered down to a single catalog
// kind by the list's own "mediatype" field.
type List struct {
	name   string
	kind   commonv1.MediaKind
	cfg    importlist.MdblistConfig
	apiKey string
	client *http.Client
}

// New returns a List. It returns ErrUnsupportedKind unless kind is movie or
// series.
func New(name string, kind commonv1.MediaKind, cfg importlist.MdblistConfig, apiKey string, opts ...Option) (*List, error) {
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindSeries {
		return nil, ErrUnsupportedKind
	}
	o := options{client: http.DefaultClient}
	for _, apply := range opts {
		apply(&o)
	}
	return &List{name: name, kind: kind, cfg: cfg, apiKey: apiKey, client: o.client}, nil
}

// Option configures a List.
type Option func(*options)

type options struct {
	client *http.Client
}

// WithHTTPClient overrides the *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList.
func (l *List) Kind() commonv1.MediaKind { return l.kind }

// wantMediaType is the mdblist "mediatype" value matching l.kind.
func (l *List) wantMediaType() string {
	if l.kind == commonv1.MediaKindSeries {
		return "show"
	}
	return "movie"
}

// Fetch implements importlist.ImportList.
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.mdblist.fetch")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.URL, nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	q := req.URL.Query()
	q.Set("apikey", l.apiKey)
	q.Set("limit", "1000")
	req.URL.RawQuery = q.Encode()

	resp, err := l.client.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("importlist/mdblist: unexpected status %d", resp.StatusCode)
		tracing.RecordError(span, err)
		return nil, err
	}

	var rows []struct {
		Title string `json:"title"`
		IDs   struct {
			Imdb string `json:"imdb"`
			Tmdb int    `json:"tmdb"`
			Tvdb int    `json:"tvdb"`
		} `json:"ids"`
		Mediatype   string `json:"mediatype"`
		ReleaseYear int32  `json:"release_year"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		err = fmt.Errorf("importlist/mdblist: decode: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	want := l.wantMediaType()
	items := make([]importlist.Item, 0, len(rows))
	for _, row := range rows {
		if row.Mediatype != want {
			logging.FromContext(ctx).Debug("importlist/mdblist: skipping row of another mediatype",
				"title", row.Title, "mediatype", row.Mediatype, "want", want)
			continue
		}
		items = append(items, importlist.Item{
			Title: row.Title,
			Year:  row.ReleaseYear,
			ExternalIDs: importlist.ExternalIDs{
				IMDb: row.IDs.Imdb,
				TMDB: importlist.NonZeroString(row.IDs.Tmdb),
				TVDB: importlist.NonZeroString(row.IDs.Tvdb),
			},
		})
	}
	return items, nil
}
