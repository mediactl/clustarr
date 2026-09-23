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

// Package fanart is a metadata.ArtworkProvider for fanart.tv
// (docs/research/metadata.md §2.3): logos, clear art, backgrounds, discs
// and banners for movies, series, artists and albums -- the *arrs'
// "clearlogo" comes from here.
//
// It speaks API v3.2 (https://webservice.fanart.tv/v3.2/{movies/{tmdb or
// imdb}, tv/{tvdb}, music/{mb-artist}, music/albums/{mb-release-group}}),
// the version fanart.tv's own client defaults to and Jellyfin's fanart
// plugin requests. The shapes are the official client's
// (github.com/fanart-tv/fanart.tv-api, src/index.d.ts and README): every
// image is {id, url, lang, likes, added, width, height} with each number a
// string, a season image adds "season" ("all" or a number), a disc image
// "disc" and "disc_type", and "lang" is "00" for a textless image. v3.2
// returns an artist's albums as an array of {release_group_id, albumcover,
// cdart}; v3 and v3.1 returned an object keyed by release-group id, and
// both are accepted. The key is sent as ?api_key= (and a personal
// ?client_key= when configured); an unknown key answers 401
// {"error":"invalid API key"}, verified against the live service.
package fanart

import (
	"context"
	"encoding/json"
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
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is fanart.tv's API root; the version is part of each path.
const DefaultBaseURL = "https://webservice.fanart.tv"

const apiVersion = "/v3.2"

// DefaultRate and DefaultBurst are the limit a caller should give this
// client when its MetadataProvider sets none. fanart.tv documents every
// key tier as "Unlimited" (fanart.tv-api README, "Personal API Keys") but
// does answer 429 with Retry-After; two requests a second is chosen
// politeness, not a published number.
const (
	DefaultRate  rate.Limit = 2
	DefaultBurst int        = 4
)

// ErrNoAPIKey is returned by New without an API key: fanart.tv refuses
// every request without one.
var ErrNoAPIKey = errors.New("fanart: an api key is required")

// ErrInvalidID is returned, before any request is made, for an id whose
// shape is wrong for the key it arrived under.
var ErrInvalidID = errors.New("fanart: invalid id")

var (
	numericPattern = regexp.MustCompile(`^[0-9]+$`)
	imdbPattern    = regexp.MustCompile(`^tt[0-9]{7,10}$`)
	mbidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// probeMovie is The Matrix, the same permanent TMDB id the TMDB prober
// uses. A 404 would prove reachability as well; a 401 is what Ping exists
// to surface.
const probeMovie = "603"

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// APIKey is the project key; required.
	APIKey string
	// ClientKey is an optional personal key, which shortens how long a new
	// image takes to appear (7 days to 2, per the fanart.tv-api README).
	ClientKey string
}

// Client is a metadata.ArtworkProvider backed by fanart.tv.
type Client struct {
	h         *httpjson.Client
	baseURL   string
	apiKey    string
	clientKey string
}

// New builds a Client, refusing a Config without an API key.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		h:         &httpjson.Client{Provider: "fanart", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		baseURL:   base,
		apiKey:    cfg.APIKey,
		clientKey: cfg.ClientKey,
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "fanart" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{
		LookupBy: []string{metadata.KeyTMDB, metadata.KeyIMDb, metadata.KeyTVDB, metadata.KeyMBArtist, metadata.KeyMBReleaseGroup},
		Artwork:  true,
	}
}

// image is one fanart.tv image; every number arrives as a string.
type image struct {
	URL    string `json:"url"`
	Lang   string `json:"lang"`
	Season string `json:"season"`
	Width  string `json:"width"`
	Height string `json:"height"`
}

// typeMap files each fanart.tv image-type key under the metadata.ImageType
// it is. A key not listed -- characterart, and any type fanart.tv adds
// later -- has no ImageType and is left out rather than filed under the
// nearest one.
var typeMap = map[string]metadata.ImageType{
	// movies
	"movieposter":       metadata.ImageTypePoster,
	"moviebackground":   metadata.ImageTypeFanart,
	"movie4kbackground": metadata.ImageTypeFanart,
	"hdmovielogo":       metadata.ImageTypeLogo,
	"movielogo":         metadata.ImageTypeLogo,
	"hdmovieclearart":   metadata.ImageTypeClearart,
	"movieart":          metadata.ImageTypeClearart,
	"moviedisc":         metadata.ImageTypeDisc,
	"moviebanner":       metadata.ImageTypeBanner,
	"moviethumb":        metadata.ImageTypeThumb,
	// tv
	"tvposter":         metadata.ImageTypePoster,
	"seasonposter":     metadata.ImageTypePoster,
	"showbackground":   metadata.ImageTypeFanart,
	"show4kbackground": metadata.ImageTypeFanart,
	"hdtvlogo":         metadata.ImageTypeLogo,
	"clearlogo":        metadata.ImageTypeLogo,
	"hdclearart":       metadata.ImageTypeClearart,
	"clearart":         metadata.ImageTypeClearart,
	"tvthumb":          metadata.ImageTypeThumb,
	"seasonthumb":      metadata.ImageTypeThumb,
	"tvbanner":         metadata.ImageTypeBanner,
	"seasonbanner":     metadata.ImageTypeBanner,
	// music artist
	"artistthumb":        metadata.ImageTypeThumb,
	"artistbackground":   metadata.ImageTypeFanart,
	"artist4kbackground": metadata.ImageTypeFanart,
	"hdmusiclogo":        metadata.ImageTypeLogo,
	"musiclogo":          metadata.ImageTypeLogo,
	"musicbanner":        metadata.ImageTypeBanner,
	// music album
	"albumcover": metadata.ImageTypePoster,
	"cdart":      metadata.ImageTypeDisc,
}

// Artwork returns fanart.tv's images for one entity: a movie by
// ids[KeyTMDB] (else ids[KeyIMDb]), a series by ids[KeyTVDB], an artist by
// ids[KeyMBArtist] (the artist's own images, not its albums'), or an album
// by ids[KeyMBReleaseGroup]. Any other kind is ErrUnsupported.
//
// Images come back grouped by type in the order fanart.tv lists them,
// which is its own popularity order; a caller that wants one poster takes
// the first.
func (c *Client) Artwork(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) ([]metadata.Image, error) {
	ctx, span := tracing.Start(ctx, "metadata.fanart.Artwork")
	defer span.End()

	path, albumID, err := resolvePath(kind, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := c.h.GetJSON(ctx, c.url(path), nil, &raw); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	if albumID == "" {
		return c.images(raw)
	}
	album, err := findAlbum(raw["albums"], albumID)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("fanart: album %s: %w", albumID, err)
	}
	return c.images(album)
}

// Ping proves the key is accepted. A 404 for the probe movie still proves
// that; a 401 is ErrAuth.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.fanart.Ping")
	defer span.End()
	_, err := c.Artwork(ctx, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: probeMovie})
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

func (c *Client) url(path string) string {
	q := url.Values{"api_key": {c.apiKey}}
	if c.clientKey != "" {
		q.Set("client_key", c.clientKey)
	}
	return c.baseURL + apiVersion + path + "?" + q.Encode()
}

// resolvePath picks the endpoint for kind and ids, validating the id's
// shape before it is spliced into the path. albumID is set only for an
// album, whose images are one entry of the response's albums list.
func resolvePath(kind commonv1.MediaKind, ids metadata.ExternalIDs) (path, albumID string, err error) {
	switch kind {
	case commonv1.MediaKindMovie:
		if id := ids[metadata.KeyTMDB]; id != "" {
			if !numericPattern.MatchString(id) {
				return "", "", fmt.Errorf("%w: tmdb %q", ErrInvalidID, id)
			}
			return "/movies/" + id, "", nil
		}
		if id := ids[metadata.KeyIMDb]; id != "" {
			if !imdbPattern.MatchString(id) {
				return "", "", fmt.Errorf("%w: imdb %q", ErrInvalidID, id)
			}
			return "/movies/" + id, "", nil
		}
		return "", "", fmt.Errorf("fanart: a movie needs %q or %q in ExternalIDs: %w", metadata.KeyTMDB, metadata.KeyIMDb, metadata.ErrUnsupported)
	case commonv1.MediaKindSeries:
		id := ids[metadata.KeyTVDB]
		if id == "" {
			return "", "", fmt.Errorf("fanart: a series needs %q in ExternalIDs: %w", metadata.KeyTVDB, metadata.ErrUnsupported)
		}
		if !numericPattern.MatchString(id) {
			return "", "", fmt.Errorf("%w: tvdb %q", ErrInvalidID, id)
		}
		return "/tv/" + id, "", nil
	case commonv1.MediaKindArtist:
		id := ids[metadata.KeyMBArtist]
		if id == "" {
			return "", "", fmt.Errorf("fanart: an artist needs %q in ExternalIDs: %w", metadata.KeyMBArtist, metadata.ErrUnsupported)
		}
		if !mbidPattern.MatchString(id) {
			return "", "", fmt.Errorf("%w: mb-artist %q", ErrInvalidID, id)
		}
		return "/music/" + id, "", nil
	case commonv1.MediaKindAlbum:
		id := ids[metadata.KeyMBReleaseGroup]
		if id == "" {
			return "", "", fmt.Errorf("fanart: an album needs %q in ExternalIDs: %w", metadata.KeyMBReleaseGroup, metadata.ErrUnsupported)
		}
		if !mbidPattern.MatchString(id) {
			return "", "", fmt.Errorf("%w: mb-release-group %q", ErrInvalidID, id)
		}
		return "/music/albums/" + id, id, nil
	default:
		return "", "", fmt.Errorf("fanart: no artwork for kind %q: %w", kind, metadata.ErrUnsupported)
	}
}

// findAlbum returns the entry of albums -- a v3.2 array or a v3 object --
// whose release group is id. An album fanart.tv lists no images for is
// ErrNotFound.
func findAlbum(albums json.RawMessage, id string) (map[string]json.RawMessage, error) {
	if len(albums) == 0 || string(albums) == "null" {
		return nil, metadata.ErrNotFound
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(albums, &list); err == nil {
		for _, a := range list {
			var rg string
			if raw, ok := a["release_group_id"]; ok && json.Unmarshal(raw, &rg) == nil && rg == id {
				return a, nil
			}
		}
		return nil, metadata.ErrNotFound
	}
	var byID map[string]map[string]json.RawMessage
	if err := json.Unmarshal(albums, &byID); err != nil {
		return nil, fmt.Errorf("%w: albums is neither a list nor an object: %w", metadata.ErrDecode, err)
	}
	a, ok := byID[id]
	if !ok {
		return nil, metadata.ErrNotFound
	}
	return a, nil
}

// images maps every known image-type key of one response object.
// Iteration follows typeMap's keys in a fixed order so the result is
// deterministic.
func (c *Client) images(obj map[string]json.RawMessage) ([]metadata.Image, error) {
	var out []metadata.Image
	for _, key := range orderedKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var imgs []image
		if err := json.Unmarshal(raw, &imgs); err != nil {
			return nil, fmt.Errorf("fanart: %s: %w: %w", key, metadata.ErrDecode, err)
		}
		for _, img := range imgs {
			if img.URL == "" {
				continue
			}
			m := metadata.Image{Type: typeMap[key], URL: img.URL}
			if img.Lang != "00" {
				m.Language = img.Lang
			}
			if w, err := strconv.Atoi(img.Width); err == nil {
				m.Width = w
			}
			if h, err := strconv.Atoi(img.Height); err == nil {
				m.Height = h
			}
			// "all" (every season) and an absent season both leave Season
			// nil: an image for the whole series.
			if s, err := strconv.ParseInt(img.Season, 10, 32); err == nil {
				season := int32(s)
				m.Season = &season
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// orderedKeys is typeMap's keys in the order images are emitted: posters
// first, then backgrounds, logos, clear art, discs, banners, thumbs.
var orderedKeys = []string{
	"movieposter", "tvposter", "seasonposter", "albumcover",
	"moviebackground", "movie4kbackground", "showbackground", "show4kbackground", "artistbackground", "artist4kbackground",
	"hdmovielogo", "movielogo", "hdtvlogo", "clearlogo", "hdmusiclogo", "musiclogo",
	"hdmovieclearart", "movieart", "hdclearart", "clearart",
	"moviedisc", "cdart",
	"moviebanner", "tvbanner", "seasonbanner", "musicbanner",
	"moviethumb", "tvthumb", "seasonthumb", "artistthumb",
}

var _ metadata.ArtworkProvider = (*Client)(nil)
