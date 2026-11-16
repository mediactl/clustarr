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

// Package wikipedia is the keyless biography source
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §5.4):
// from an artist's or author's Wikidata or Wikipedia link (a MusicBrainz
// url-rel, an Open Library remote id) to the English Wikipedia summary
// of the page the link names, in two requests at most -- the Wikidata
// entity's sitelink (wbgetentities, props=sitelinks) and the page summary
// (REST v1 page/summary, whose "extract" is the lead section as plain
// text). MusicBrainz annotations are sparse (Muse's is empty), and Plex's
// artist page and the artist.nfo's <biography> want one.
//
// Wikimedia's public limit is 200 requests a second per client with a
// descriptive User-Agent (api.wikimedia.org/wiki/Rate_limits); the caller
// still injects the limiter, as every client here does.
package wikipedia

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The hosts the client reads, overridable for tests.
const (
	DefaultWikidataBaseURL  = "https://www.wikidata.org"
	DefaultWikipediaBaseURL = "https://en.wikipedia.org"
	DefaultCommonsBaseURL   = "https://commons.wikimedia.org"
	// DefaultLanguage is the Wikipedia edition read; the Wikidata sitelink
	// asked for is "<language>wiki".
	DefaultLanguage = "en"
)

// Source is what Artist.BiographySource records for a biography from here.
const Source = "wikipedia"

// The link types a MusicBrainz url-rel (and an Open Library remote id, as
// the gateway names them) carry.
const (
	LinkWikidata  = "wikidata"
	LinkWikipedia = "wikipedia"
)

// Config configures a Client. No key: Wikimedia's APIs are public.
type Config struct {
	HTTPClient       *http.Client
	WikidataBaseURL  string
	WikipediaBaseURL string
	CommonsBaseURL   string
	// Language is the Wikipedia edition ("en"); the Wikipedia base URL must
	// serve it.
	Language string
	// Limiter is the caller's; nil means no limiting here.
	Limiter   *rate.Limiter
	UserAgent string
}

// Client reads biographies.
type Client struct {
	h                       *httpjson.Client
	wikidata, wiki, commons string
	language                string
}

// New builds a Client.
func New(cfg Config) *Client {
	wd := strings.TrimRight(cfg.WikidataBaseURL, "/")
	if wd == "" {
		wd = DefaultWikidataBaseURL
	}
	wp := strings.TrimRight(cfg.WikipediaBaseURL, "/")
	if wp == "" {
		wp = DefaultWikipediaBaseURL
	}
	cm := strings.TrimRight(cfg.CommonsBaseURL, "/")
	if cm == "" {
		cm = DefaultCommonsBaseURL
	}
	lang := cfg.Language
	if lang == "" {
		lang = DefaultLanguage
	}
	return &Client{
		h:        &httpjson.Client{Provider: "wikipedia", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		wikidata: wd, wiki: wp, commons: cm, language: lang,
	}
}

// Name implements metadata.Provider.
func (c *Client) Name() string { return "wikipedia" }

// Capabilities implements metadata.Provider: no lookup by id and no
// search; the client answers only through the links another provider found.
func (c *Client) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }

var (
	entityRE                            = regexp.MustCompile(`^Q[1-9][0-9]*$`)
	_        metadata.BiographyProvider = (*Client)(nil)
)

// Biography implements metadata.BiographyProvider: the summary of the
// Wikipedia page links names, by a wikipedia link first (its title is the
// page), else a wikidata link (its entity's sitelink names the page);
// metadata.ErrNotFound when links name neither, the entity has no page in
// the language, or the page is gone.
func (c *Client) Biography(ctx context.Context, links []metadata.Link) (string, string, error) {
	ctx, span := tracing.Start(ctx, "wikipedia.Biography")
	defer span.End()
	title, err := c.title(ctx, links)
	if err != nil {
		return "", "", err
	}
	text, err := c.summary(ctx, title)
	if err != nil {
		return "", "", err
	}
	return text, Source, nil
}

// title is the page title the links lead to.
func (c *Client) title(ctx context.Context, links []metadata.Link) (string, error) {
	for _, l := range links {
		if l.Type == LinkWikipedia {
			if t := pageTitle(l.URL, c.language); t != "" {
				return t, nil
			}
		}
	}
	for _, l := range links {
		if l.Type != LinkWikidata {
			continue
		}
		id := entityID(l.URL)
		if id == "" {
			continue
		}
		return c.sitelink(ctx, id)
	}
	return "", fmt.Errorf("wikipedia: no wikipedia or wikidata link: %w", metadata.ErrNotFound)
}

// pageTitle is the title a Wikipedia URL of the language names, "" for
// another edition's or anything else.
func pageTitle(raw, language string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, language+".wikipedia.org") || !strings.HasPrefix(u.Path, "/wiki/") {
		return ""
	}
	t, err := url.PathUnescape(strings.TrimPrefix(u.Path, "/wiki/"))
	if err != nil || t == "" {
		return ""
	}
	return strings.ReplaceAll(t, "_", " ")
}

// entityID is the Q-id a Wikidata URL names, "" for anything else.
func entityID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Host), "wikidata.org") {
		return ""
	}
	id := strings.TrimPrefix(u.Path, "/wiki/")
	id = strings.TrimPrefix(id, "/entity/")
	if !entityRE.MatchString(id) {
		return ""
	}
	return id
}

type entities struct {
	Entities map[string]struct {
		Missing   *string `json:"missing"`
		Sitelinks map[string]struct {
			Title string `json:"title"`
		} `json:"sitelinks"`
	} `json:"entities"`
}

// sitelink is the entity's page title in the client's language.
func (c *Client) sitelink(ctx context.Context, id string) (string, error) {
	q := url.Values{"action": {"wbgetentities"}, "ids": {id}, "props": {"sitelinks"}, "sitefilter": {c.language + "wiki"}, "format": {"json"}}
	var out entities
	if err := c.h.GetJSON(ctx, c.wikidata+"/w/api.php?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	e, ok := out.Entities[id]
	if !ok || e.Missing != nil {
		return "", fmt.Errorf("wikipedia: wikidata entity %s: %w", id, metadata.ErrNotFound)
	}
	sl, ok := e.Sitelinks[c.language+"wiki"]
	if !ok || sl.Title == "" {
		return "", fmt.Errorf("wikipedia: %s has no %s page: %w", id, c.language, metadata.ErrNotFound)
	}
	return sl.Title, nil
}

type summary struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Extract string `json:"extract"`
}

// summary is the page's lead section as plain text; a disambiguation page
// is no biography.
func (c *Client) summary(ctx context.Context, title string) (string, error) {
	path := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	var out summary
	if err := c.h.GetJSON(ctx, c.wiki+"/api/rest_v1/page/summary/"+path, nil, &out); err != nil {
		return "", err
	}
	text := strings.TrimSpace(out.Extract)
	if text == "" || out.Type == "disambiguation" {
		return "", fmt.Errorf("wikipedia: %q has no summary: %w", title, metadata.ErrNotFound)
	}
	return text, nil
}

// PortraitWidth is the width an artist's photo is asked for at: a square
// card or page shows it at most this wide, and Commons answers its nearest
// rendition (1280 for a larger photo) or, for a smaller one, the file.
const PortraitWidth = 1000

// Portrait implements metadata.PortraitProvider: the image (P18) of the
// Wikidata entity the first wikidata link names, at Commons' rendition of
// PortraitWidth -- two requests, the entity's image claim and the file's
// imageinfo -- as a poster. An uploaded original runs to many megabytes
// and is never asked for.
func (c *Client) Portrait(ctx context.Context, links []metadata.Link) (metadata.Image, error) {
	ctx, span := tracing.Start(ctx, "metadata.wikipedia.Portrait")
	defer span.End()
	id := ""
	for _, l := range links {
		if l.Type == LinkWikidata {
			if id = entityID(l.URL); id != "" {
				break
			}
		}
	}
	if id == "" {
		return metadata.Image{}, fmt.Errorf("wikipedia: no wikidata link: %w", metadata.ErrNotFound)
	}
	file, err := c.imageClaim(ctx, id)
	if err != nil {
		tracing.RecordError(span, err)
		return metadata.Image{}, err
	}
	img, err := c.rendition(ctx, file)
	if err != nil {
		tracing.RecordError(span, err)
	}
	return img, err
}

type claims struct {
	Claims map[string][]struct {
		Mainsnak struct {
			Datavalue struct {
				Value any `json:"value"`
			} `json:"datavalue"`
		} `json:"mainsnak"`
	} `json:"claims"`
}

// imageClaim is the file name of the entity's first image (P18).
func (c *Client) imageClaim(ctx context.Context, id string) (string, error) {
	q := url.Values{"action": {"wbgetclaims"}, "entity": {id}, "property": {"P18"}, "format": {"json"}}
	var out claims
	if err := c.h.GetJSON(ctx, c.wikidata+"/w/api.php?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	for _, cl := range out.Claims["P18"] {
		if name, ok := cl.Mainsnak.Datavalue.Value.(string); ok && name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("wikipedia: wikidata entity %s has no image: %w", id, metadata.ErrNotFound)
}

type imageInfo struct {
	Query struct {
		Pages map[string]struct {
			ImageInfo []struct {
				URL         string `json:"url"`
				ThumbURL    string `json:"thumburl"`
				ThumbWidth  int    `json:"thumbwidth"`
				ThumbHeight int    `json:"thumbheight"`
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
}

// rendition is Commons' rendition of file at PortraitWidth, with Commons'
// tracking parameters dropped.
func (c *Client) rendition(ctx context.Context, file string) (metadata.Image, error) {
	q := url.Values{
		"action": {"query"}, "titles": {"File:" + file}, "prop": {"imageinfo"},
		"iiprop": {"url|size"}, "iiurlwidth": {fmt.Sprint(PortraitWidth)}, "format": {"json"},
	}
	var out imageInfo
	if err := c.h.GetJSON(ctx, c.commons+"/w/api.php?"+q.Encode(), nil, &out); err != nil {
		return metadata.Image{}, err
	}
	for _, page := range out.Query.Pages {
		for _, ii := range page.ImageInfo {
			u := ii.ThumbURL
			if u == "" {
				u = ii.URL
			}
			if u == "" {
				continue
			}
			return metadata.Image{Type: metadata.ImageTypePoster, URL: withoutQuery(u), Width: ii.ThumbWidth, Height: ii.ThumbHeight}, nil
		}
	}
	return metadata.Image{}, fmt.Errorf("wikipedia: commons has no file %q: %w", file, metadata.ErrNotFound)
}

// withoutQuery drops a URL's query: Commons appends utm_ tracking
// parameters, which name no different image.
func withoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	return u.String()
}

var _ metadata.PortraitProvider = (*Client)(nil)
