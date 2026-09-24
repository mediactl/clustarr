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

// Package mdblist is a metadata.RatingsProvider for MDBList
// (https://api.mdblist.com), the aggregator spec §C.2 names for every
// rating source TMDB does not supply: imdb, tmdb, Rotten Tomatoes critic and
// audience, metacritic, trakt and letterboxd, for movies and series.
//
// The shapes are the ones recorded from the live API on 2026-09-24 into
// test/data/metadata/mdblist/ (docs/research/ratings-providers.md):
// GET /tmdb/movie/{id}, /tmdb/show/{id} and /tvdb/show/{id} answer the same
// document, whose ratings[] carries {source, value, score, votes, url} with
// value on the source's own scale and any of value, votes or url null (url
// is sometimes a number); an unknown id answers 404 {"error":"Item not
// found"} and a rejected key 401 {"error":"Invalid API key"} (403 on
// /user). The key travels as ?apikey=. Every response carries
// X-RateLimit-Limit, X-RateLimit-Remaining and X-RateLimit-Reset (Unix
// seconds): the quota is per key per day, 1000 on the free plan. GET /user
// reports the same quota and does not spend it, which makes it the probe.
package mdblist

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is MDBList's API root.
const DefaultBaseURL = "https://api.mdblist.com"

// DefaultRate and DefaultBurst are the limit a caller should give this
// client when its MetadataProvider sets none. MDBList publishes a daily
// quota per key, not a per-second rate; two requests a second is chosen
// politeness.
const (
	DefaultRate  rate.Limit = 2
	DefaultBurst int        = 2
)

// ErrNoAPIKey is returned by New without an API key: MDBList refuses every
// request without one.
var ErrNoAPIKey = errors.New("mdblist: an api key is required")

// ErrInvalidID is returned, before any request is made, for an id whose
// shape is wrong for the key it arrived under.
var ErrInvalidID = errors.New("mdblist: invalid id")

var numericPattern = regexp.MustCompile(`^[0-9]+$`)

// restFallback is how long a key rests after a 429 that named no reset:
// long enough not to spend the next request on a key that is out, short
// enough that a missing header does not bench it for the day.
const restFallback = time.Hour

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string

	// APIKeys are spent in order; the first is required. Each key has a
	// daily quota of its own, so a second key doubles the calls a day can
	// make: a key MDBList answers 429 for, or reports none remaining on,
	// rests until its X-RateLimit-Reset and the next key is used.
	APIKeys []string

	// Now is the clock rest periods are measured against. Nil is
	// time.Now.
	Now func() time.Time
}

// Client is a metadata.RatingsProvider backed by MDBList.
type Client struct {
	h       *httpjson.Client
	baseURL string
	keys    []string
	now     func() time.Time

	mu        sync.Mutex
	restUntil []time.Time // per key; zero is "usable"
}

// New builds a Client, refusing a Config without an API key. Empty entries
// in APIKeys after the first are dropped, so an optional second key left
// blank is no key rather than a key that fails.
func New(cfg Config) (*Client, error) {
	if len(cfg.APIKeys) == 0 || cfg.APIKeys[0] == "" {
		return nil, ErrNoAPIKey
	}
	keys := make([]string, 0, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		if k != "" {
			keys = append(keys, k)
		}
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		h:         &httpjson.Client{Provider: "mdblist", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, Now: now},
		baseURL:   base,
		keys:      keys,
		now:       now,
		restUntil: make([]time.Time, len(keys)),
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "mdblist" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB, metadata.KeyTVDB}}
}

// ratingScale files each MDBList rating source under the pkg/metadata
// source it is, with the factor from MDBList's value -- on the source's own
// scale -- to ValueCentis on the CRD's: out of 10 scaled to 0-1000 for
// imdb, tmdb, trakt and letterboxd, out of 100 scaled to 0-10000 for
// metacritic and the Rotten Tomatoes pair (catalogv1alpha1.Rating). MDBList
// reports tmdb and trakt out of 100 and letterboxd out of 5. A source not
// listed -- metacriticuser, rogerebert, myanimelist, and anything MDBList
// adds later -- has no RatingSource and is left out.
var ratingScale = map[string]struct {
	source string
	factor float64
	max    int32
}{
	"imdb":       {metadata.RatingSourceIMDb, 100, 1000},        // 8.8/10  -> 880
	"tmdb":       {metadata.RatingSourceTMDB, 10, 1000},         // 83/100  -> 830
	"trakt":      {metadata.RatingSourceTrakt, 10, 1000},        // 87/100  -> 870
	"letterboxd": {metadata.RatingSourceLetterboxd, 200, 1000},  // 4.2/5   -> 840
	"metacritic": {metadata.RatingSourceMetacritic, 100, 10000}, // 74/100  -> 7400
	"tomatoes":   {metadata.RatingSourceRTCritic, 100, 10000},   // 86/100  -> 8600
	"popcorn":    {metadata.RatingSourceRTAudience, 100, 10000}, // 91/100 -> 9100
}

// declared is every source ratingScale fills, in RatingSources' order.
var declared = []string{
	metadata.RatingSourceIMDb, metadata.RatingSourceTMDB,
	metadata.RatingSourceRTCritic, metadata.RatingSourceRTAudience,
	metadata.RatingSourceMetacritic, metadata.RatingSourceTrakt,
	metadata.RatingSourceLetterboxd,
}

// RatingSources implements metadata.RatingsProvider: every source for a
// movie or a series, none for any other kind.
func (c *Client) RatingSources(kind commonv1.MediaKind) []string {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries:
		return append([]string(nil), declared...)
	default:
		return nil
	}
}

type document struct {
	Ratings []struct {
		Source string   `json:"source"`
		Value  *float64 `json:"value"`
		Votes  *float64 `json:"votes"`
	} `json:"ratings"`
}

// Ratings implements metadata.RatingsProvider: one call, keyed by TMDB id
// for a movie and by TMDB or else TVDB id for a series. ids with neither is
// metadata.ErrUnsupported, as is any other kind.
func (c *Client) Ratings(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.Ratings, error) {
	ctx, span := tracing.Start(ctx, "metadata.mdblist.Ratings")
	defer span.End()

	path, err := resolvePath(kind, ids)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var doc document
	if err := c.h.Decode(body, &doc); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := metadata.Ratings{}
	for _, r := range doc.Ratings {
		s, ok := ratingScale[r.Source]
		if !ok || r.Value == nil {
			continue
		}
		centis := int32(min(max(math.Round(*r.Value*s.factor), 0), float64(s.max)))
		var votes int32
		if r.Votes != nil {
			votes = int32(min(max(*r.Votes, 0), math.MaxInt32))
		}
		if centis == 0 && votes == 0 {
			continue
		}
		out[s.source] = metadata.Rating{Source: s.source, ValueCentis: centis, Votes: votes}
	}
	return out, nil
}

// resolvePath picks the endpoint for kind and ids, validating the id's
// shape before it is spliced into the path.
func resolvePath(kind commonv1.MediaKind, ids metadata.ExternalIDs) (string, error) {
	var media string
	var keys []string
	switch kind {
	case commonv1.MediaKindMovie:
		media, keys = "movie", []string{metadata.KeyTMDB}
	case commonv1.MediaKindSeries:
		media, keys = "show", []string{metadata.KeyTMDB, metadata.KeyTVDB}
	default:
		return "", fmt.Errorf("mdblist: %s: %w", kind, metadata.ErrUnsupported)
	}
	for _, k := range keys {
		id := ids[k]
		if id == "" {
			continue
		}
		if !numericPattern.MatchString(id) {
			return "", fmt.Errorf("%w: %s %q", ErrInvalidID, k, id)
		}
		return "/" + k + "/" + media + "/" + id, nil
	}
	return "", fmt.Errorf("mdblist: %s without a %s id: %w", kind, strings.Join(keys, " or "), metadata.ErrUnsupported)
}

// get GETs path with the first key that is not resting, moving to the next
// on a 429 until every key rests, which is a *metadata.RateLimitedError
// naming the soonest reset.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	for {
		i, ok := c.usableKey()
		if !ok {
			return nil, &metadata.RateLimitedError{Provider: "mdblist", RetryAfter: c.soonestReset()}
		}
		u := c.url(path, c.keys[i])
		resp, err := c.h.Do(ctx, httpjson.Request{Method: http.MethodGet, URL: u})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			c.rest(i, c.resetOf(resp.Header, true))
			continue
		}
		if err := c.h.Check(resp, u); err != nil {
			return nil, err
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			c.rest(i, c.resetOf(resp.Header, false))
		}
		return resp.Body, nil
	}
}

// Ping implements the MetadataProvider probe: GET /user with every key,
// which reports the quota without spending it. A rejected key is
// metadata.ErrAuth naming its position, never its value.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.mdblist.Ping")
	defer span.End()
	for i, k := range c.keys {
		u := c.url("/user", k)
		resp, err := c.h.Do(ctx, httpjson.Request{Method: http.MethodGet, URL: u})
		if err == nil {
			err = c.h.Check(resp, u)
		}
		if err != nil {
			tracing.RecordError(span, err)
			return fmt.Errorf("mdblist: key %d of %d: %w", i+1, len(c.keys), err)
		}
	}
	return nil
}

func (c *Client) url(path, key string) string {
	return c.baseURL + path + "?" + url.Values{"apikey": {key}}.Encode()
}

func (c *Client) usableKey() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for i, t := range c.restUntil {
		if !now.Before(t) {
			return i, true
		}
	}
	return 0, false
}

func (c *Client) rest(i int, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restUntil[i] = until
}

func (c *Client) soonestReset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	soonest := c.restUntil[0]
	for _, t := range c.restUntil[1:] {
		if t.Before(soonest) {
			soonest = t
		}
	}
	return max(soonest.Sub(c.now()), 0)
}

// resetOf is when a key that is out may be used again: X-RateLimit-Reset
// when it lies in the future, else (on a 429) Retry-After, else
// restFallback from now.
func (c *Client) resetOf(h http.Header, limited bool) time.Time {
	now := c.now()
	if secs, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if t := time.Unix(secs, 0); t.After(now) {
			return t
		}
	}
	if limited {
		if d := httpjson.ParseRetryAfter(h.Get("Retry-After"), now); d > 0 {
			return now.Add(d)
		}
	}
	return now.Add(restFallback)
}
