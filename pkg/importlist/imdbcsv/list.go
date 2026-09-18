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

package imdbcsv

import (
	"context"
	"fmt"
	"net/http"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Option configures a List.
type Option func(*options)

type options struct {
	client *http.Client
}

// WithHTTPClient overrides the *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// List fetches an IMDb CSV export by URL and parses it with Parse.
type List struct {
	name   string
	kind   commonv1.MediaKind
	cfg    importlist.ImdbCSVConfig
	client *http.Client
}

// New returns a List.
func New(name string, kind commonv1.MediaKind, cfg importlist.ImdbCSVConfig, opts ...Option) (*List, error) {
	o := options{client: http.DefaultClient}
	for _, apply := range opts {
		apply(&o)
	}
	return &List{name: name, kind: kind, cfg: cfg, client: o.client}, nil
}

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList.
func (l *List) Kind() commonv1.MediaKind { return l.kind }

// Fetch implements importlist.ImportList: it GETs cfg.URL, then parses the
// response body with Parse.
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.imdbcsv.fetch")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.URL, nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("importlist/imdbcsv: unexpected status %d", resp.StatusCode)
		tracing.RecordError(span, err)
		return nil, err
	}

	items, err := Parse(resp.Body, l.kind)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return items, nil
}
