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

// Package kitsu is a metadata.IDResolver over Kitsu's mappings
// (https://kitsu.io/api/edge), "a cheap crosswalk for anime/manga"
// (docs/research/metadata.md §2.5): Kitsu records, per anime or manga,
// its TVDB series, AniDB, MyAnimeList and AniList ids -- the anime ids
// design §4.2's SeriesMetadata.ExternalIDs lists.
//
// The API is JSON:API ("Accept: application/vnd.api+json", no auth for
// reads). The shapes are the live service's, captured 2026-09-23 into
// test/data/metadata/kitsu: GET /mappings?filter[externalSite]=
// &filter[externalId]=&include=item answers {"data":[{"attributes":
// {"externalSite","externalId"},"relationships":{"item":{"data":{"type":
// "anime"|"manga","id"}}}}],"meta":{"count"}}, and GET
// /{anime,manga}/{id}/mappings lists one item's mappings. The externalSite
// values are Kitsu's own: "thetvdb/series" (a series id; "thetvdb" is a
// season-scoped "79824/1" and is not read), "anidb", "myanimelist/anime",
// "myanimelist/manga", "anilist/anime", "anilist/manga", "mangaupdates".
// An unknown item is a 404; a mapping that matches nothing is an empty
// list.
package kitsu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is Kitsu's JSON:API root.
const DefaultBaseURL = "https://kitsu.io/api/edge"

// DefaultRate and DefaultBurst are chosen, not published: Kitsu documents
// no rate limit, so one request a second keeps a crosswalk polite.
const (
	DefaultRate  rate.Limit = 1
	DefaultBurst int        = 2
)

// pageLimit is JSON:API's page[limit], which Kitsu caps at 20.
const pageLimit = 20

// maxMappingPages bounds one item's mapping pages -- 100 mappings, several
// times what any real item has.
const maxMappingPages = 5

// Sentinel errors.
var (
	// ErrAmbiguous means an id maps to more than one Kitsu item of the
	// kind asked for. Picking one would be a guess.
	ErrAmbiguous = errors.New("kitsu: more than one item matches")
	// ErrInvalidID is returned, before any request, for an id that is not
	// the positive integer its key promises.
	ErrInvalidID = errors.New("kitsu: invalid id")
)

var numericPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
}

// Client is a metadata.IDResolver backed by Kitsu.
type Client struct {
	h       *httpjson.Client
	baseURL string
}

// New builds a Client.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{h: &httpjson.Client{Provider: "kitsu", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent}, baseURL: base}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "kitsu" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{extid.KeyKitsu, metadata.KeyAniList, extid.KeyMAL, extid.KeyAniDB, metadata.KeyTVDB}}
}

var jsonAPI = http.Header{"Accept": {"application/vnd.api+json"}}

type mapping struct {
	Attributes struct {
		ExternalSite string `json:"externalSite"`
		ExternalID   string `json:"externalId"`
	} `json:"attributes"`
	Relationships struct {
		Item struct {
			Data *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"data"`
		} `json:"item"`
	} `json:"relationships"`
}

type mappingPage struct {
	Data []mapping `json:"data"`
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
}

// site is one Kitsu externalSite and the ExternalIDs key it maps to.
type site struct{ name, key string }

// sitesFor lists, for an item type, the externalSites this client reads,
// most specific first: the order Resolve tries a caller's ids in. TVDB is
// last because it is the only one several anime can share.
func sitesFor(itemType string) []site {
	if itemType == "manga" {
		return []site{{"anilist/manga", metadata.KeyAniList}, {"myanimelist/manga", extid.KeyMAL}, {"mangaupdates", extid.KeyMangaUpdates}}
	}
	return []site{{"anilist/anime", metadata.KeyAniList}, {"myanimelist/anime", extid.KeyMAL}, {"anidb", extid.KeyAniDB}, {"thetvdb/series", metadata.KeyTVDB}}
}

// Resolve crosswalks an anime (for a series or movie) or a manga (for a
// comic) through Kitsu: it finds the one Kitsu item ids names -- by
// ids[kitsu], else the first of AniList, MAL, AniDB and TVDB (for manga:
// AniList, MAL, MangaUpdates) that ids carries -- and returns every id
// Kitsu maps that item to, its own included. Any other kind, or ids with
// none of those, is ErrUnsupported without a request.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.kitsu.Resolve")
	defer span.End()

	itemType, ok := itemTypeFor(kind)
	if !ok {
		return nil, fmt.Errorf("kitsu: no crosswalk for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	id, err := c.findItem(ctx, itemType, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out, err := c.itemIDs(ctx, itemType, id)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return out, nil
}

func itemTypeFor(kind commonv1.MediaKind) (string, bool) {
	switch kind {
	case commonv1.MediaKindSeries, commonv1.MediaKindMovie:
		return "anime", true
	case commonv1.MediaKindComic:
		return "manga", true
	default:
		return "", false
	}
}

func (c *Client) findItem(ctx context.Context, itemType string, ids metadata.ExternalIDs) (string, error) {
	if k := ids[extid.KeyKitsu]; k != "" {
		if !numericPattern.MatchString(k) {
			return "", fmt.Errorf("%w: kitsu %q", ErrInvalidID, k)
		}
		return k, nil
	}
	for _, s := range sitesFor(itemType) {
		v := ids[s.key]
		if v == "" {
			continue
		}
		if s.key != extid.KeyMangaUpdates && !numericPattern.MatchString(v) {
			return "", fmt.Errorf("%w: %s %q", ErrInvalidID, s.key, v)
		}
		q := url.Values{"filter[externalSite]": {s.name}, "filter[externalId]": {v}, "include": {"item"}}
		var p mappingPage
		if err := c.h.GetJSON(ctx, c.baseURL+"/mappings?"+q.Encode(), jsonAPI, &p); err != nil {
			return "", err
		}
		items := map[string]bool{}
		for _, m := range p.Data {
			if d := m.Relationships.Item.Data; d != nil && d.Type == itemType {
				items[d.ID] = true
			}
		}
		switch len(items) {
		case 0:
			return "", fmt.Errorf("kitsu: no %s mapped from %s %s: %w", itemType, s.name, v, metadata.ErrNotFound)
		case 1:
			for id := range items {
				return id, nil
			}
		default:
			return "", fmt.Errorf("%w: %d %s items map from %s %s", ErrAmbiguous, len(items), itemType, s.name, v)
		}
	}
	return "", fmt.Errorf("kitsu: needs a kitsu, anilist, mal, anidb or tvdb id: %w", metadata.ErrUnsupported)
}

// itemIDs reads every mapping of one Kitsu item into ExternalIDs. A
// mapping whose value is not the shape its key promises is left out.
func (c *Client) itemIDs(ctx context.Context, itemType, id string) (metadata.ExternalIDs, error) {
	keyOf := map[string]string{}
	for _, s := range sitesFor(itemType) {
		keyOf[s.name] = s.key
	}
	out := metadata.ExternalIDs{extid.KeyKitsu: id}
	for pageNo := 0; ; pageNo++ {
		if pageNo == maxMappingPages {
			return nil, fmt.Errorf("kitsu: %s %s: mappings still paging after %d pages", itemType, id, maxMappingPages)
		}
		q := url.Values{"page[limit]": {strconv.Itoa(pageLimit)}, "page[offset]": {strconv.Itoa(pageNo * pageLimit)}}
		var p mappingPage
		if err := c.h.GetJSON(ctx, c.baseURL+"/"+itemType+"/"+id+"/mappings?"+q.Encode(), jsonAPI, &p); err != nil {
			return nil, err
		}
		for _, m := range p.Data {
			key, ok := keyOf[m.Attributes.ExternalSite]
			v := m.Attributes.ExternalID
			if !ok || v == "" || (key != extid.KeyMangaUpdates && !numericPattern.MatchString(v)) {
				continue
			}
			if _, dup := out[key]; !dup {
				out[key] = v
			}
		}
		if len(p.Data) < pageLimit || (pageNo+1)*pageLimit >= p.Meta.Count {
			return out, nil
		}
	}
}

// probeAniDB is AniDB anime 1, Crest of the Stars: the mapping filter
// answers 200 whether or not it matches, so the probe proves reachability
// without depending on Kitsu's data.
const probeAniDB = "1"

// Ping runs one mapping filter.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.kitsu.Ping")
	defer span.End()
	q := url.Values{"filter[externalSite]": {"anidb"}, "filter[externalId]": {probeAniDB}, "page[limit]": {"1"}}
	var p mappingPage
	if err := c.h.GetJSON(ctx, c.baseURL+"/mappings?"+q.Encode(), jsonAPI, &p); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

var _ metadata.IDResolver = (*Client)(nil)
