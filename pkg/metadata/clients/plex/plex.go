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

// Package plex resolves items to their ids in Plex's own metadata service
// (metadata.provider.plex.tv), whose plex:// GUIDs Plex Web keys its
// Watchlist and Discover features on
// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md §2).
//
// The shapes are the ones recorded from the live service on 2026-10-06
// into test/data/metadata/plex/ (trimmed to the fields read here):
// GET /library/metadata/matches?type=1|2&guid=<scheme>://<id>&includeGuids=1
// answers a MediaContainer whose Metadata[] carry guid
// ("plex://movie/<24 hex>") and Guid[] (the external ids); no match is 200
// with no Metadata. /library/metadata/<id>/children lists a show's seasons
// (index = season number) and /grandchildren its episodes (parentIndex,
// index, Guid[] with tvdb://<episode id>), paged by X-Plex-Container-Start
// and -Size against totalSize. Every request needs a Plex account token in
// X-Plex-Token; without one the service answers 401.
package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is Plex's metadata service.
const DefaultBaseURL = "https://metadata.provider.plex.tv"

// DefaultRate and DefaultBurst are the limit a caller should give this
// client when its MetadataProvider sets none. Plex publishes no rate; two
// requests a second is chosen politeness.
const (
	DefaultRate  rate.Limit = 2
	DefaultBurst int        = 2
)

// DefaultPageSize is how many seasons or episodes one page asks for. An
// episode is ~7 KB untrimmed, so 100 stays far under
// metadata.MaxResponseBytes (8 MiB).
const DefaultPageSize = 100

// maxPages bounds one listing: 40 pages of 100 is 4,000 episodes, above the
// longest show in the owner's library with room to spare.
const maxPages = 40

// Cache lifetimes: a Plex id does not change, a show's episode list grows
// as episodes air, and a miss is retried daily.
const (
	idTTL       = 7 * 24 * time.Hour
	childrenTTL = 12 * time.Hour
	missTTL     = 24 * time.Hour
	cacheSize   = 8192
)

// ErrNoToken is returned by New without a token: Plex answers every
// request without one 401.
var ErrNoToken = errors.New("plex: a Plex token is required")

var idPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// Token is a Plex account token, sent as X-Plex-Token.
	Token string
	// PageSize overrides DefaultPageSize (tests page a short show).
	PageSize int
	// Clock times the client's cache; nil is the real clock.
	Clock clockwork.Clock
}

// Client resolves Plex ids. It is a metadata.IDResolver (a Movie's or
// Series' KeyPlex) and a metadata.PlexProvider (a show's seasons and
// episodes).
type Client struct {
	h        *httpjson.Client
	baseURL  string
	token    string
	pageSize int
	cache    *metadata.LRUCache
}

// New builds a Client, refusing a Config without a token.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrNoToken
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	size := cfg.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	clock := cfg.Clock
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	cache, err := metadata.NewLRUCache(cacheSize, clock)
	if err != nil {
		return nil, err
	}
	return &Client{
		h:        &httpjson.Client{Provider: "plex", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, Now: clock.Now},
		baseURL:  base,
		token:    cfg.Token,
		pageSize: size,
		cache:    cache,
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "plex" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB, metadata.KeyTVDB, metadata.KeyIMDb}}
}

// item is one entry of a MediaContainer. guid and Guid are distinct JSON
// keys; encoding/json matches each tag exactly before falling back to a
// case-insensitive match, so the two fields do not collide.
type item struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	AiredAt     string `json:"originallyAvailableAt"`
	Index       *int32 `json:"index"`
	ParentIndex *int32 `json:"parentIndex"`
	Guid        []struct {
		ID string `json:"id"`
	} `json:"Guid"`
}

type container struct {
	MediaContainer struct {
		Offset    int    `json:"offset"`
		Size      int    `json:"size"`
		TotalSize int    `json:"totalSize"`
		Metadata  []item `json:"Metadata"`
	} `json:"MediaContainer"`
}

func (c *Client) header() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("X-Plex-Token", c.token)
	return h
}

// plexID is it's id when its guid is plex://<typ>/<24 hex>.
func plexID(it item, typ string) (string, bool) {
	id, ok := strings.CutPrefix(it.GUID, "plex://"+typ+"/")
	return id, ok && idPattern.MatchString(id)
}

// externalID is it's Guid[] value for scheme ("tvdb" -> "297989"), "" for
// none.
func externalID(it item, scheme string) string {
	for _, g := range it.Guid {
		if v, ok := strings.CutPrefix(g.ID, scheme+"://"); ok {
			return v
		}
	}
	return ""
}

// match asks Plex for the item of plexType carrying guid and accepts only a
// single result that carries guid among its own ids.
func (c *Client) match(ctx context.Context, plexType int, typ, guid string) (string, error) {
	q := url.Values{"type": {strconv.Itoa(plexType)}, "guid": {guid}, "includeGuids": {"1"}}
	var out container
	if err := c.h.GetJSON(ctx, c.baseURL+"/library/metadata/matches?"+q.Encode(), c.header(), &out); err != nil {
		return "", err
	}
	ms := out.MediaContainer.Metadata
	if len(ms) != 1 {
		return "", fmt.Errorf("plex: %s: %d results: %w", guid, len(ms), metadata.ErrNotFound)
	}
	scheme, id, _ := strings.Cut(guid, "://")
	if externalID(ms[0], scheme) != id {
		return "", fmt.Errorf("plex: %s: the result is another item: %w", guid, metadata.ErrNotFound)
	}
	pid, ok := plexID(ms[0], typ)
	if !ok {
		return "", fmt.Errorf("plex: %s: unexpected guid %q: %w", guid, ms[0].GUID, metadata.ErrNotFound)
	}
	return pid, nil
}

// itemID is a Movie's or Series' Plex id, asked by each of its ids in turn
// (a movie by tmdb then imdb, a show by tvdb, tmdb then imdb), each answer
// and each miss cached.
func (c *Client) itemID(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (string, error) {
	var (
		plexType int
		typ      string
		keys     []string
	)
	switch kind {
	case commonv1.MediaKindMovie:
		plexType, typ, keys = 1, "movie", []string{metadata.KeyTMDB, metadata.KeyIMDb}
	case commonv1.MediaKindSeries:
		plexType, typ, keys = 2, "show", []string{metadata.KeyTVDB, metadata.KeyTMDB, metadata.KeyIMDb}
	default:
		return "", fmt.Errorf("plex: no Plex id for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	for _, k := range keys {
		v := ids[k]
		if v == "" {
			continue
		}
		guid := k + "://" + v
		key := "id/" + typ + "/" + guid
		var cached string
		if hit, _ := c.cache.Get(ctx, key, &cached); hit {
			if cached == "" {
				continue
			}
			return cached, nil
		}
		id, err := c.match(ctx, plexType, typ, guid)
		switch {
		case err == nil:
			_ = c.cache.Set(ctx, key, id, idTTL)
			return id, nil
		case errors.Is(err, metadata.ErrNotFound):
			_ = c.cache.Set(ctx, key, "", missTTL)
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("plex: no Plex %s for %v: %w", typ, ids, metadata.ErrNotFound)
}

// Resolve answers KeyPlex for a Movie or Series (metadata.IDResolver).
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.Resolve")
	defer span.End()
	id, err := c.itemID(ctx, kind, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return metadata.ExternalIDs{metadata.KeyPlex: id}, nil
}

// ShowChildren answers a show's Plex id and its seasons' and episodes'
// (metadata.PlexProvider), cached per show.
func (c *Client) ShowChildren(ctx context.Context, ids metadata.ExternalIDs) (*metadata.PlexChildren, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.ShowChildren")
	defer span.End()
	show, err := c.itemID(ctx, commonv1.MediaKindSeries, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	key := "children/" + show
	var cached metadata.PlexChildren
	if hit, _ := c.cache.Get(ctx, key, &cached); hit {
		return &cached, nil
	}
	seasons, err := c.pages(ctx, "/library/metadata/"+show+"/children", nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	episodes, err := c.pages(ctx, "/library/metadata/"+show+"/grandchildren", url.Values{"includeGuids": {"1"}})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := metadata.PlexChildren{ShowID: show}
	for _, s := range seasons {
		if id, ok := plexID(s, "season"); ok && s.Index != nil {
			out.Seasons = append(out.Seasons, metadata.PlexSeason{Number: *s.Index, ID: id})
		}
	}
	for _, e := range episodes {
		id, ok := plexID(e, "episode")
		if !ok || e.Index == nil || e.ParentIndex == nil {
			continue
		}
		out.Episodes = append(out.Episodes, metadata.PlexEpisode{
			Season: *e.ParentIndex, Episode: *e.Index, TVDB: externalID(e, metadata.KeyTVDB), ID: id,
			Title: e.Title, AirDate: e.AiredAt,
		})
	}
	_ = c.cache.Set(ctx, key, out, childrenTTL)
	return &out, nil
}

// pages reads every page of a listing, refusing one longer than maxPages.
func (c *Client) pages(ctx context.Context, path string, q url.Values) ([]item, error) {
	var all []item
	for range maxPages {
		v := url.Values{}
		for k, vs := range q {
			v[k] = vs
		}
		v.Set("X-Plex-Container-Start", strconv.Itoa(len(all)))
		v.Set("X-Plex-Container-Size", strconv.Itoa(c.pageSize))
		var out container
		if err := c.h.GetJSON(ctx, c.baseURL+path+"?"+v.Encode(), c.header(), &out); err != nil {
			return nil, err
		}
		mc := out.MediaContainer
		all = append(all, mc.Metadata...)
		if len(mc.Metadata) == 0 || len(all) >= mc.TotalSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("plex: %s: more than %d pages", path, maxPages)
}

// Ping asks for one well-known film (The Matrix): the cheapest call that
// proves the service reachable and the token accepted.
func (c *Client) Ping(ctx context.Context) error {
	q := url.Values{"type": {"1"}, "guid": {"tmdb://603"}}
	var out container
	return c.h.GetJSON(ctx, c.baseURL+"/library/metadata/matches?"+q.Encode(), c.header(), &out)
}

var (
	_ metadata.IDResolver   = (*Client)(nil)
	_ metadata.PlexProvider = (*Client)(nil)
)
