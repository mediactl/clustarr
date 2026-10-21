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

// Package custom is a pkg/importlist provider for an arbitrary feed. An RSS
// feed is read: each item's ids come from its guid and from the IMDb and
// TMDB links in its link and description, as mdblist.com's list feeds carry
// them (guid show:<tmdb id>). A JSON feed is deferred past M6 (spec §17
// "Deferred") and its Fetch returns a typed importlist.NotImplementedError.
package custom

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/redact"
)

// MaxFeedBytes caps a feed's body. mdblist's feed of a 164-show list is
// about 200 KB.
const MaxFeedBytes = 16 << 20

// ErrResponseTooLarge is a feed whose body passes MaxFeedBytes.
var ErrResponseTooLarge = errors.New("importlist/custom: feed is larger than the cap")

// List is an ImportList over a custom feed.
type List struct {
	name   string
	kind   commonv1.MediaKind
	cfg    importlist.CustomConfig
	client *http.Client
}

// Option configures a List.
type Option func(*List)

// WithHTTPClient sets the client the feed is fetched with.
func WithHTTPClient(c *http.Client) Option { return func(l *List) { l.client = c } }

// New returns a List.
func New(name string, kind commonv1.MediaKind, cfg importlist.CustomConfig, opts ...Option) *List {
	l := &List{name: name, kind: kind, cfg: cfg, client: http.DefaultClient}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList.
func (l *List) Kind() commonv1.MediaKind { return l.kind }

// Fetch implements importlist.ImportList. A feed that is not RSS (the
// format defaults to JSON) returns a *importlist.NotImplementedError.
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	if l.cfg.Format != importlist.CustomListFormatRSS {
		return nil, &importlist.NotImplementedError{
			Provider: "custom",
			TODO:     "Custom JSON feed fetch — deferred past M6, see docs/research/metadata.md §3.5",
		}
	}
	ctx, span := tracing.Start(ctx, "importlist.custom.fetch")
	defer span.End()
	body, err := l.get(ctx)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	items, err := l.parseRSS(ctx, body)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return items, nil
}

func (l *List) get(ctx context.Context) ([]byte, error) {
	// A feed URL can carry its list's token (mdblist's ?rss=), and
	// http.Client.Do's *url.Error quotes the URL whole: redact.Err drops
	// the query and keeps the cause.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.URL, nil)
	if err != nil {
		return nil, redact.Err(err)
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9, */*;q=0.1")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, redact.Err(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("importlist/custom: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("importlist/custom: read the feed: %w", redact.Err(err))
	}
	if len(body) > MaxFeedBytes {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

type rssFeed struct {
	XMLName xml.Name  `xml:"rss"`
	Items   []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
}

var (
	// mdblist's guid: the item's TMDB id and its media type.
	mdblistGUID = regexp.MustCompile(`^(show|movie):(\d+)$`)
	imdbLink    = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
	tmdbLink    = regexp.MustCompile(`themoviedb\.org/(tv|movie)/(\d+)`)
	titleYear   = regexp.MustCompile(`^(.*\S)\s+\((\d{4})\)$`)
)

func (l *List) parseRSS(ctx context.Context, body []byte) ([]importlist.Item, error) {
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("importlist/custom: not an RSS feed: %w", err)
	}
	items := make([]importlist.Item, 0, len(feed.Items))
	for _, it := range feed.Items {
		item, mediaKind := parseItem(it)
		if mediaKind != "" && mediaKind != l.kind {
			logging.FromContext(ctx).Debug("importlist/custom: skipping an item of another kind",
				"title", item.Title, "kind", mediaKind, "want", l.kind)
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// parseItem is an item's title, year and ids, and the kind its TMDB id is
// one of ("" when the item does not say).
func parseItem(it rssItem) (importlist.Item, commonv1.MediaKind) {
	var item importlist.Item
	item.Title = strings.TrimSpace(it.Title)
	if m := titleYear.FindStringSubmatch(item.Title); m != nil {
		year, _ := strconv.ParseInt(m[2], 10, 32)
		item.Title, item.Year = m[1], int32(year)
	}
	var kind commonv1.MediaKind
	if m := mdblistGUID.FindStringSubmatch(strings.TrimSpace(it.GUID)); m != nil {
		item.ExternalIDs.TMDB, kind = m[2], mediaKindOf(m[1])
	}
	text := it.Link + "\n" + it.Description
	if m := imdbLink.FindStringSubmatch(text); m != nil {
		item.ExternalIDs.IMDb = m[1]
	}
	if item.ExternalIDs.TMDB == "" {
		if m := tmdbLink.FindStringSubmatch(text); m != nil {
			item.ExternalIDs.TMDB, kind = m[2], mediaKindOf(m[1])
		}
	}
	return item, kind
}

func mediaKindOf(s string) commonv1.MediaKind {
	if s == "movie" {
		return commonv1.MediaKindMovie
	}
	return commonv1.MediaKindSeries
}
