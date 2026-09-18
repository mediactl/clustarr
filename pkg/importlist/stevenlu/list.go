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

// Package stevenlu is a pkg/importlist provider for the Steven Lu
// popular-movies feed (https://github.com/JustinRDavis/popular-movies), a
// static JSON document with no settings and no auth. It covers movies
// only -- the feed has no other kind.
package stevenlu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// defaultURL is the production Steven Lu feed.
const defaultURL = "https://popular-movies-data.stevenlu.com/movies.json"

// Option configures a List.
type Option func(*options)

type options struct {
	url    string
	client *http.Client
}

// WithBaseURL overrides the feed URL, for tests.
func WithBaseURL(u string) Option { return func(o *options) { o.url = u } }

// WithHTTPClient overrides the *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// List fetches the Steven Lu popular-movies feed.
type List struct {
	name string
	opts options
}

// New returns a List using defaultURL and http.DefaultClient unless
// overridden by opts.
func New(name string, opts ...Option) *List {
	o := options{url: defaultURL, client: http.DefaultClient}
	for _, apply := range opts {
		apply(&o)
	}
	return &List{name: name, opts: o}
}

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList. It is always movie -- the feed has
// no other kind.
func (l *List) Kind() commonv1.MediaKind { return commonv1.MediaKindMovie }

// Fetch implements importlist.ImportList.
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.stevenlu.fetch")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.opts.url, nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	resp, err := l.opts.client.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("importlist/stevenlu: unexpected status %d", resp.StatusCode)
		tracing.RecordError(span, err)
		return nil, err
	}

	var rows []struct {
		Title  string `json:"title"`
		ImdbID string `json:"imdb_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		err = fmt.Errorf("importlist/stevenlu: decode: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	items := make([]importlist.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, importlist.Item{Title: row.Title, ExternalIDs: importlist.ExternalIDs{IMDb: row.ImdbID}})
	}
	return items, nil
}
