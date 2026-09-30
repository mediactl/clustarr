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

// Package theintrodb is a client for TheIntroDB's v3 API
// (https://api.theintrodb.org/v3/media): community skip segments -- intro,
// recap, credits and preview -- per title, per episode for TV, told apart
// by release through the file's duration.
//
// It needs no key: anonymous callers get 30 requests per 10 s and a usage
// allowance (x-usagelimit-*), and a key, sent as a Bearer token, raises
// both. A 429 is a metadata.RateLimitedError: it carries the server's
// Retry-After, or, since the API sends none (2026-09-30), the reset of
// whichever limit is spent.
package theintrodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is TheIntroDB's API host.
const DefaultBaseURL = "https://api.theintrodb.org"

// DefaultRate and DefaultBurst stay inside the anonymous 30 requests per
// 10 seconds the API reports in x-ratelimit-limit (2026-09-30).
const (
	DefaultRate  rate.Limit = 2
	DefaultBurst int        = 2
)

// pingTMDB is The Matrix, which TheIntroDB has: Ping asks for it.
const pingTMDB = "603"

// Config configures a Client. The caller owns rate limiting.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// APIKeys are spent in order: each has an allowance of its own, and a
	// key that is out rests until its reset while the next is used. None
	// is anonymous.
	APIKeys []string
	// Now is the clock rest periods are measured against; nil is time.Now.
	Now func() time.Time
}

// Client queries TheIntroDB.
type Client struct {
	h       *httpjson.Client
	baseURL string
	keys    []string // "" is anonymous
	now     func() time.Time

	mu        sync.Mutex
	restUntil []time.Time // per key; zero is usable
}

// New builds a Client.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	var keys []string
	for _, k := range cfg.APIKeys {
		if k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		keys = []string{""}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		h:         &httpjson.Client{Provider: "theintrodb", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, Now: now},
		baseURL:   base,
		keys:      keys,
		now:       now,
		restUntil: make([]time.Time, len(keys)),
	}, nil
}

// Name implements metadata.Provider.
func (c *Client) Name() string { return "theintrodb" }

// Capabilities implements metadata.Provider.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB, metadata.KeyTVDB, metadata.KeyIMDb}}
}

type rawSegment struct {
	StartMs *int64 `json:"start_ms"`
	EndMs   *int64 `json:"end_ms"`
}

type rawMedia struct {
	Intro   []rawSegment `json:"intro"`
	Recap   []rawSegment `json:"recap"`
	Credits []rawSegment `json:"credits"`
	Preview []rawSegment `json:"preview"`
}

// Markers implements metadata.MarkersProvider. It asks by TMDB id, else
// TVDB, else IMDb; ids naming none of them are ErrNotFound without a
// request.
func (c *Client) Markers(ctx context.Context, q metadata.MarkersQuery) (metadata.Segments, error) {
	ctx, span := tracing.Start(ctx, "metadata.theintrodb.Markers")
	defer span.End()
	v := url.Values{}
	switch {
	case q.IDs[metadata.KeyTMDB] != "":
		v.Set("tmdb_id", q.IDs[metadata.KeyTMDB])
	case q.IDs[metadata.KeyTVDB] != "":
		v.Set("tvdb_id", q.IDs[metadata.KeyTVDB])
	case q.IDs[metadata.KeyIMDb] != "":
		v.Set("imdb_id", q.IDs[metadata.KeyIMDb])
	default:
		return metadata.Segments{}, fmt.Errorf("theintrodb: no tmdb, tvdb or imdb id: %w", metadata.ErrNotFound)
	}
	if q.Season > 0 || q.Episode > 0 {
		v.Set("season", strconv.Itoa(int(q.Season)))
		v.Set("episode", strconv.Itoa(int(q.Episode)))
	}
	if q.DurationMs > 0 {
		v.Set("duration_ms", strconv.FormatInt(q.DurationMs, 10))
	}
	var raw rawMedia
	if err := c.get(ctx, c.baseURL+"/v3/media?"+v.Encode(), &raw); err != nil {
		tracing.RecordError(span, err)
		return metadata.Segments{}, err
	}
	return metadata.Segments{
		Intro:   resolve(raw.Intro, q.DurationMs),
		Recap:   resolve(raw.Recap, q.DurationMs),
		Credits: resolve(raw.Credits, q.DurationMs),
		Preview: resolve(raw.Preview, q.DurationMs),
	}, nil
}

// Ping asks for a title TheIntroDB is known to have.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Markers(ctx, metadata.MarkersQuery{IDs: metadata.ExternalIDs{metadata.KeyTMDB: pingTMDB}})
	return err
}

// get GETs rawURL with the first key not resting, moving to the next on a
// 429 until every key rests, which is a *metadata.RateLimitedError naming
// the soonest reset. A 429's wait is Retry-After, else the reset of the
// spent limit, since the API sends no Retry-After (2026-09-30).
func (c *Client) get(ctx context.Context, rawURL string, out any) error {
	for {
		i, ok := c.usableKey()
		if !ok {
			return &metadata.RateLimitedError{Provider: "theintrodb", RetryAfter: c.soonestReset()}
		}
		resp, err := c.h.Do(ctx, httpjson.Request{Method: http.MethodGet, URL: rawURL, Header: c.header(c.keys[i])})
		if err != nil {
			return err
		}
		if err := c.h.Check(resp, rawURL); err != nil {
			var rl *metadata.RateLimitedError
			if errors.As(err, &rl) {
				wait := rl.RetryAfter
				if wait == 0 {
					wait = limitReset(resp.Header)
				}
				c.rest(i, wait)
				continue
			}
			if errors.Is(err, metadata.ErrNotFound) && noTitle(resp.Body) {
				return fmt.Errorf("theintrodb: %s: %w", httpjson.Redact(rawURL), metadata.ErrNoTitle)
			}
			return err
		}
		if resp.Header.Get("X-Usagelimit-Remaining") == "0" {
			c.rest(i, limitReset(resp.Header))
		}
		return c.h.Decode(resp.Body, out)
	}
}

// restFallback is how long a key rests when TheIntroDB names no reset.
const restFallback = time.Minute

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

func (c *Client) rest(i int, d time.Duration) {
	if d <= 0 {
		d = restFallback
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restUntil[i] = c.now().Add(d)
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

// noTitle reports whether a 404's body says the title is missing, not
// just the season or episode asked for ("media not found for provided
// season/episode").
func noTitle(body []byte) bool {
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &e) == nil && e.Error == "media not found"
}

// limitReset is the reset, in seconds, of the spent limit: the usage
// allowance's, else the rate window's; zero when neither says it is spent.
// A 200 reporting none remaining counts as spent.
func limitReset(h http.Header) time.Duration {
	for _, l := range []string{"X-Usagelimit", "X-Ratelimit"} {
		if h.Get(l+"-Remaining") != "0" {
			continue
		}
		if secs, err := strconv.Atoi(h.Get(l + "-Reset")); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func (c *Client) header(key string) http.Header {
	h := http.Header{"Accept": []string{"application/json"}}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return h
}

// resolve places each segment in the file: a null start is its beginning,
// a null end its end (dropped when the duration is unknown), and an empty
// or inverted segment -- TheIntroDB's {null, 0} for "none" -- is dropped.
func resolve(raw []rawSegment, durationMs int64) []metadata.Segment {
	var out []metadata.Segment
	for _, r := range raw {
		var s metadata.Segment
		if r.StartMs != nil {
			s.StartMs = *r.StartMs
		}
		switch {
		case r.EndMs != nil:
			s.EndMs = *r.EndMs
		case durationMs > 0:
			s.EndMs = durationMs
		default:
			continue
		}
		if s.StartMs < 0 || s.EndMs <= s.StartMs {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Keys are a MetadataProvider Secret's keys: apiKey, then each line of
// apiKeys, blank lines and repeats dropped.
func Keys(apiKey, apiKeys string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range append([]string{apiKey}, strings.Split(apiKeys, "\n")...) {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
