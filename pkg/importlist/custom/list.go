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

// Package custom is a pkg/importlist provider for an arbitrary feed.
//
//   - An RSS feed: each item's ids come from its guid and from the IMDb and
//     TMDB links in its link and description, as mdblist.com's list feeds
//     carry them (guid show:<tmdb id>). A private mdblist list's feed needs
//     the list's own ?rss= token.
//   - A JSON feed (2026-10-28): mdblist.com's list JSON,
//     https://mdblist.com/lists/<user>/<slug>/json, an array of
//     {id (TMDB), tvdbid, imdb_id, title, mediatype show|movie,
//     release_year}. It needs no token for a public list; a private one
//     answers {"error": "This List is Private"}, which fails the fetch.
package custom

import (
	"context"
	"encoding/json"
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

// Fetch implements importlist.ImportList: the feed read as RSS, or as
// mdblist's list JSON (the format's default).
func (l *List) Fetch(ctx context.Context) ([]importlist.Item, error) {
	ctx, span := tracing.Start(ctx, "importlist.custom.fetch")
	defer span.End()
	body, err := l.get(ctx)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var items []importlist.Item
	if l.cfg.Format == importlist.CustomListFormatRSS {
		items, err = l.parseRSS(ctx, body)
	} else {
		items, err = l.parseJSON(ctx, body)
	}
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return items, nil
}

// jsonItem is one item of mdblist's list JSON.
type jsonItem struct {
	ID          int64  `json:"id"` // the TMDB id
	TVDBID      *int64 `json:"tvdbid"`
	IMDbID      string `json:"imdb_id"`
	Title       string `json:"title"`
	MediaType   string `json:"mediatype"` // show or movie
	ReleaseYear int32  `json:"release_year"`
}

// parseJSON reads mdblist's list JSON. An object in place of the array is
// mdblist's error ({"error": "This List is Private"}), and fails the fetch
// rather than reading as an empty list, which would remove every item.
func (l *List) parseJSON(ctx context.Context, body []byte) ([]importlist.Item, error) {
	var raw []jsonItem
	if err := json.Unmarshal(body, &raw); err != nil {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("importlist/custom: the list answered %q", e.Error)
		}
		return nil, fmt.Errorf("importlist/custom: not mdblist's list JSON: %w", err)
	}
	items := make([]importlist.Item, 0, len(raw))
	for _, it := range raw {
		kind := mediaKindOf(it.MediaType)
		if kind != l.kind {
			logging.FromContext(ctx).Debug("importlist/custom: skipping an item of another kind",
				"title", it.Title, "kind", kind, "want", l.kind)
			continue
		}
		item := importlist.Item{Title: strings.TrimSpace(it.Title), Year: it.ReleaseYear}
		if it.ID > 0 {
			item.ExternalIDs.TMDB = strconv.FormatInt(it.ID, 10)
		}
		if it.TVDBID != nil && *it.TVDBID > 0 {
			item.ExternalIDs.TVDB = strconv.FormatInt(*it.TVDBID, 10)
		}
		if strings.HasPrefix(it.IMDbID, "tt") {
			item.ExternalIDs.IMDb = it.IMDbID
		}
		items = append(items, item)
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
	accept := "application/rss+xml, application/xml;q=0.9, */*;q=0.1"
	if l.cfg.Format != importlist.CustomListFormatRSS {
		accept = "application/json, */*;q=0.1"
	}
	req.Header.Set("Accept", accept)
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
