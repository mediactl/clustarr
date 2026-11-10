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

// Package audible searches Audible's public catalog
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §5.6,
// Q2): GET /1.0/catalog/products?title=&num_results=&response_groups= on
// the marketplace's host, keyless and undocumented but stable for years
// (Audnexus and the legacy Plex audiobook agents built on it). It answers
// Add New's audiobook search -- title, authors, narrators, release, cover,
// ASIN -- and nothing else; Audnexus, keyed by ASIN, answers the rest.
// Recorded on 2026-11-10: test/data/metadata/audible.
package audible

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultRegion is the marketplace searched when none is named.
const DefaultRegion = "us"

// MaxResults bounds one search.
const MaxResults = 25

// Hosts are the catalog hosts per Audible marketplace (the AudiobookRegion
// enum's values).
var Hosts = map[string]string{
	"us": "https://api.audible.com",
	"uk": "https://api.audible.co.uk",
	"ca": "https://api.audible.ca",
	"au": "https://api.audible.com.au",
	"de": "https://api.audible.de",
	"fr": "https://api.audible.fr",
	"es": "https://api.audible.es",
	"in": "https://api.audible.in",
	"it": "https://api.audible.it",
	"jp": "https://api.audible.co.jp",
}

// Config configures a Client. No key.
type Config struct {
	HTTPClient *http.Client
	// BaseURL overrides the marketplace host for every region (tests).
	BaseURL string
	// Limiter is the caller's; nil means no limiting here.
	Limiter   *rate.Limiter
	UserAgent string
}

// Client searches the catalog.
type Client struct {
	h       *httpjson.Client
	baseURL string
}

// New builds a Client.
func New(cfg Config) *Client {
	return &Client{
		h:       &httpjson.Client{Provider: "audible", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
	}
}

// Name implements metadata.Provider.
func (c *Client) Name() string { return "audible" }

// Capabilities implements metadata.Provider: search only.
func (c *Client) Capabilities() metadata.Capabilities { return metadata.Capabilities{Search: true} }

var _ metadata.AudiobookSearcher = (*Client)(nil)

// Product is one catalog entry as the search answers it.
type Product struct {
	ASIN        string
	Title       string
	Subtitle    string
	Authors     []metadata.NamedRef
	Narrators   []string
	Publisher   string
	ReleaseDate *time.Time
	Runtime     time.Duration
	Language    string
	Abridged    bool
	IsAdult     bool
	Summary     string
	Series      *metadata.SeriesLink
	Image       string
	Rating      *metadata.Rating
}

type response struct {
	Products []struct {
		ASIN      string                        `json:"asin"`
		Title     string                        `json:"title"`
		Subtitle  string                        `json:"subtitle"`
		Authors   []struct{ ASIN, Name string } `json:"authors"`
		Narrators []struct{ Name string }       `json:"narrators"`
		Publisher string                        `json:"publisher_name"`
		Release   string                        `json:"release_date"`
		Runtime   int64                         `json:"runtime_length_min"`
		Language  string                        `json:"language"`
		Format    string                        `json:"format_type"`
		IsAdult   bool                          `json:"is_adult_product"`
		Summary   string                        `json:"merchandising_summary"`
		Images    map[string]string             `json:"product_images"`
		Series    []struct {
			Title    string `json:"title"`
			Sequence string `json:"sequence"`
		} `json:"series"`
		Rating *struct {
			Overall struct {
				Average float64 `json:"average_rating"`
				Count   int32   `json:"num_ratings"`
			} `json:"overall_distribution"`
		} `json:"rating"`
	} `json:"products"`
	Total int `json:"total_results"`
}

// Products searches region's catalog by title (and author when given),
// at most MaxResults, Audible's relevance order.
func (c *Client) Products(ctx context.Context, title, author, region string) ([]Product, error) {
	ctx, span := tracing.Start(ctx, "audible.Products")
	defer span.End()
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("audible: empty title: %w", metadata.ErrNotFound)
	}
	base, err := c.host(region)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"title":            {title},
		"num_results":      {strconv.Itoa(MaxResults)},
		"products_sort_by": {"Relevance"},
		"response_groups":  {"product_desc,contributors,media,product_attrs,series,rating"},
	}
	if a := strings.TrimSpace(author); a != "" {
		q.Set("author", a)
	}
	var out response
	if err := c.h.GetJSON(ctx, base+"/1.0/catalog/products?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	products := make([]Product, 0, len(out.Products))
	for _, p := range out.Products {
		if p.ASIN == "" || p.Title == "" {
			continue
		}
		prod := Product{
			ASIN: p.ASIN, Title: p.Title, Subtitle: p.Subtitle, Publisher: p.Publisher,
			Language: p.Language, Abridged: strings.EqualFold(p.Format, "abridged"), IsAdult: p.IsAdult,
			Summary: stripTags(p.Summary), Image: p.Images["500"],
		}
		if prod.Image == "" {
			for _, u := range p.Images {
				prod.Image = u
				break
			}
		}
		for _, a := range p.Authors {
			prod.Authors = append(prod.Authors, metadata.NamedRef{Name: a.Name, ASIN: a.ASIN})
		}
		for _, n := range p.Narrators {
			prod.Narrators = append(prod.Narrators, n.Name)
		}
		if t, err := time.Parse("2006-01-02", p.Release); err == nil {
			prod.ReleaseDate = &t
		}
		if p.Runtime > 0 {
			prod.Runtime = time.Duration(p.Runtime) * time.Minute
		}
		if len(p.Series) > 0 && p.Series[0].Title != "" {
			prod.Series = &metadata.SeriesLink{Series: p.Series[0].Title, Position: p.Series[0].Sequence, Primary: true}
		}
		if p.Rating != nil && p.Rating.Overall.Count > 0 {
			prod.Rating = &metadata.Rating{Source: "audible", ValueCentis: int32(p.Rating.Overall.Average*100 + 0.5), Votes: p.Rating.Overall.Count}
		}
		products = append(products, prod)
	}
	return products, nil
}

// SearchAudiobooks implements metadata.AudiobookSearcher: the products as
// Add New's hits, keyed by ASIN, each carrying its authors (with their
// Audible author ASINs, which the author link looks up through Audnexus;
// 2026-11-10 design D11), narrators, publisher and release date (§9.5).
func (c *Client) SearchAudiobooks(ctx context.Context, q string, region string) ([]metadata.SearchHit, error) {
	products, err := c.Products(ctx, q, "", region)
	if err != nil {
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(products))
	for _, p := range products {
		h := metadata.SearchHit{
			IDs:         metadata.ExternalIDs{metadata.KeyASIN: p.ASIN},
			Title:       p.Title,
			Poster:      p.Image,
			Publisher:   p.Publisher,
			Authors:     p.Authors,
			Narrators:   p.Narrators,
			ReleaseDate: p.ReleaseDate,
		}
		if p.ReleaseDate != nil {
			h.Year = int32(p.ReleaseDate.UTC().Year())
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// host is the catalog host of region: the override, else Hosts' entry.
func (c *Client) host(region string) (string, error) {
	if c.baseURL != "" {
		return c.baseURL, nil
	}
	if region == "" {
		region = DefaultRegion
	}
	h, ok := Hosts[strings.ToLower(region)]
	if !ok {
		return "", fmt.Errorf("audible: unknown region %q", region)
	}
	return h, nil
}

// stripTags drops the markup Audible's merchandising summary carries.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}
