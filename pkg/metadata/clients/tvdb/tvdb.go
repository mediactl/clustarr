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

// Package tvdb is an in-house metadata.SeriesProvider for TheTVDB v4 REST
// API (https://api4.thetvdb.com/v4). TheTVDB has no adopted Go client
// module (docs/research/metadata.md §2.2; the Phase B dependency pre-add
// list does not include one), so every request goes through net/http
// directly, including the apikey+pin -> bearer JWT login flow in auth.go.
package tvdb

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://api4.thetvdb.com/v4"

// waitOnLimiter is c.limiter.Wait, indirected through a package variable so
// a white-box test (limiter_internal_test.go) can count how many times
// doRequest waits on the limiter -- once per request attempt, including the
// retry after a 401 -- without this package's public constructor growing a
// test-only seam.
var waitOnLimiter = func(ctx context.Context, l *rate.Limiter) error {
	return l.Wait(ctx)
}

// Client is a metadata.SeriesProvider backed by TheTVDB v4.
type Client struct {
	authState

	http    *http.Client
	baseURL string
	apiKey  string
	pin     string
	limiter *rate.Limiter
}

// New builds a Client. httpClient may be nil, in which case http.DefaultClient
// is used. baseURL overrides the default TheTVDB v4 host -- tests pass an
// httptest.Server URL; production callers pass "".
func New(apiKey, pin string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		http:    hc,
		baseURL: baseURL,
		apiKey:  apiKey,
		pin:     pin,
		limiter: limiter,
	}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "tvdb" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTVDB}, Changes: true}
}

// seriesExtendedResponse is TheTVDB v4's SeriesExtendedRecord envelope.
type seriesExtendedResponse struct {
	Data struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		Overview   string `json:"overview"`
		FirstAired string `json:"firstAired"`
		LastAired  string `json:"lastAired"`
		Status     struct {
			Name string `json:"name"`
		} `json:"status"`
		OriginalCountry  string `json:"originalCountry"`
		OriginalLanguage string `json:"originalLanguage"`
		AverageRuntime   int32  `json:"averageRuntime"`
		AirsTime         string `json:"airsTime"`
		RemoteIDs        []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			SourceName string `json:"sourceName"`
		} `json:"remoteIds"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		// Image is the series' own poster; Artworks the rest, each typed
		// by /artwork/types' ids (artworkTypes).
		Image    string `json:"image"`
		Artworks []struct {
			Type  int    `json:"type"`
			Image string `json:"image"`
		} `json:"artworks"`
	} `json:"data"`
}

// artworkTypes maps TheTVDB v4's artwork type ids (/artwork/types) onto
// the normalized image types; an id not listed here is left out.
var artworkTypes = map[int]metadata.ImageType{
	1: metadata.ImageTypeBanner,
	2: metadata.ImageTypePoster,
	3: metadata.ImageTypeFanart,
	6: metadata.ImageTypeClearart,
	7: metadata.ImageTypeLogo,
}

// Series fetches a single series' extended record from
// /series/{id}/extended.
func (c *Client) Series(ctx context.Context, tvdbID string) (*metadata.Series, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Series")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw seriesExtendedResponse
	if err := c.doRequest(ctx, http.MethodGet, "/series/"+tvdbID+"/extended", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "tvdb: series fetch failed", "tvdb_id", tvdbID, "error", err)
		return nil, err
	}

	s := &metadata.Series{
		IDs:              metadata.ExternalIDs{metadata.KeyTVDB: strconv.FormatInt(raw.Data.ID, 10)},
		Title:            raw.Data.Name,
		Overview:         raw.Data.Overview,
		Status:           mapSeriesStatus(raw.Data.Status.Name),
		OriginalLanguage: originalLanguage(raw.Data.OriginalLanguage),
		AirTime:          raw.Data.AirsTime,
		Runtime:          raw.Data.AverageRuntime,
	}
	for _, r := range raw.Data.RemoteIDs {
		switch r.SourceName {
		case "IMDB":
			s.IDs[metadata.KeyIMDb] = r.ID
		case "TheMovieDB.com":
			s.IDs[metadata.KeyTMDB] = r.ID
		}
	}
	for _, g := range raw.Data.Genres {
		s.Genres = append(s.Genres, g.Name)
	}
	// The series' own image is its poster and comes first, so a consumer
	// that takes the first poster (the library page) shows the one TheTVDB
	// itself leads with; the artworks follow in the order published.
	if raw.Data.Image != "" {
		s.Images = append(s.Images, metadata.Image{Type: metadata.ImageTypePoster, URL: raw.Data.Image})
	}
	for _, a := range raw.Data.Artworks {
		if t, ok := artworkTypes[a.Type]; ok && a.Image != "" {
			s.Images = append(s.Images, metadata.Image{Type: t, URL: a.Image})
		}
	}
	if t, ok := parseDate(raw.Data.FirstAired); ok {
		s.FirstAired = &t
		s.Year = int32(t.Year())
	}
	if t, ok := parseDate(raw.Data.LastAired); ok {
		s.LastAired = &t
	}

	logger.DebugContext(ctx, "tvdb: series fetched", "tvdb_id", tvdbID, "title", s.Title)
	return s, nil
}

// mapSeriesStatus maps TheTVDB's status.name to metadata.SeriesStatus.
func mapSeriesStatus(name string) metadata.SeriesStatus {
	switch name {
	case "Continuing":
		return metadata.SeriesStatusContinuing
	case "Ended":
		return metadata.SeriesStatusEnded
	case "Upcoming":
		return metadata.SeriesStatusUpcoming
	default:
		return ""
	}
}

// episodesResponse is TheTVDB v4's episode-list envelope, shared by every
// season-order endpoint (/series/{id}/episodes/{order}).
type episodesResponse struct {
	Data struct {
		Episodes []struct {
			Name           string `json:"name"`
			Aired          string `json:"aired"`
			Runtime        int32  `json:"runtime"`
			Overview       string `json:"overview"`
			SeasonNumber   int32  `json:"seasonNumber"`
			Number         int32  `json:"number"`
			AbsoluteNumber *int32 `json:"absoluteNumber"`
		} `json:"episodes"`
	} `json:"data"`
	// Links pages the list: v4 returns page_size (500) episodes per page
	// and names the next page in next, null on the last.
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// Episodes fetches a series' episode list under the given season order
// (one of SeasonOrder's values: default|official|dvd|absolute|alternate|
// regional -- passed through verbatim, the caller's to validate).
func (c *Client) Episodes(ctx context.Context, tvdbID string, order string) ([]metadata.Episode, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Episodes")
	defer span.End()
	logger := logging.FromContext(ctx)

	// The list is paged (episodesResponse.Links): every page is fetched, by
	// number rather than by following links.next's own text, until next is
	// null. An empty page ends the walk whatever next says, so a provider
	// that kept naming one could not run the limiter's budget down on
	// nothing.
	var episodes []metadata.Episode
	base := "/series/" + tvdbID + "/episodes/" + order
	for page := 0; ; page++ {
		var raw episodesResponse
		path := base + "?page=" + strconv.Itoa(page)
		if err := c.doRequest(ctx, http.MethodGet, path, &raw); err != nil {
			tracing.RecordError(span, err)
			logger.ErrorContext(ctx, "tvdb: episodes fetch failed", "tvdb_id", tvdbID, "order", order, "page", page, "error", err)
			return nil, err
		}
		for _, e := range raw.Data.Episodes {
			ep := metadata.Episode{
				SeasonNumber:   e.SeasonNumber,
				EpisodeNumber:  e.Number,
				AbsoluteNumber: e.AbsoluteNumber,
				Title:          e.Name,
				Overview:       e.Overview,
				Runtime:        e.Runtime,
			}
			if t, ok := parseDate(e.Aired); ok {
				ep.AirDate = &t
			}
			episodes = append(episodes, ep)
		}
		if len(raw.Data.Episodes) == 0 || raw.Links.Next == nil || *raw.Links.Next == "" {
			break
		}
	}

	logger.DebugContext(ctx, "tvdb: episodes fetched", "tvdb_id", tvdbID, "order", order, "count", len(episodes))
	return episodes, nil
}

// updatesResponse is TheTVDB v4's /updates envelope.
type updatesResponse struct {
	Data []struct {
		RecordID int64 `json:"recordId"`
	} `json:"data"`
}

// Updates returns the record ids changed since since, across series,
// episodes and seasons.
func (c *Client) Updates(ctx context.Context, since time.Time) ([]string, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Updates")
	defer span.End()
	logger := logging.FromContext(ctx)

	path := fmt.Sprintf("/updates?since=%d&type=series,episodes,seasons", since.Unix())
	var raw updatesResponse
	if err := c.doRequest(ctx, http.MethodGet, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "tvdb: updates fetch failed", "error", err)
		return nil, err
	}

	ids := make([]string, 0, len(raw.Data))
	for _, u := range raw.Data {
		ids = append(ids, strconv.FormatInt(u.RecordID, 10))
	}
	return ids, nil
}

var _ metadata.SeriesProvider = (*Client)(nil)

// parseDate parses a TheTVDB "YYYY-MM-DD" date; an empty or malformed
// string reports ok=false rather than a zero time, so callers leave the
// corresponding *time.Time nil instead of storing a bogus 0001-01-01.
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// doRequest waits on the rate limiter, authenticates lazily on the first
// call, sets the bearer token, and on a 401 response authenticates exactly
// once more and retries the request once before giving up with
// metadata.ErrAuth. A 200 body is read through metadata.DecodeJSON's cap.
func (c *Client) doRequest(ctx context.Context, method, path string, out any) error {
	if err := waitOnLimiter(ctx, c.limiter); err != nil {
		return err
	}
	if c.currentToken() == "" {
		if err := c.authenticate(ctx); err != nil {
			return err
		}
	}

	resp, err := c.rawRequest(ctx, method, path)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if err := c.authenticate(ctx); err != nil {
			return err
		}
		// The retried request is a second, distinct call against the
		// provider and must draw its own token from the limiter -- the
		// first Wait above only covers the request that came back 401.
		if err := waitOnLimiter(ctx, c.limiter); err != nil {
			return err
		}
		resp, err = c.rawRequest(ctx, method, path)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			return metadata.ErrAuth
		}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := metadata.DecodeJSON(resp.Body, metadata.MaxResponseBytes, out); err != nil {
			return fmt.Errorf("tvdb: %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "tvdb", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("tvdb: unexpected status %d for %s", resp.StatusCode, path)
	}
}

func (c *Client) rawRequest(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("tvdb: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.currentToken())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tvdb: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// parseRetryAfter parses a Retry-After header value (seconds) into a
// duration, defaulting to zero when absent or malformed.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// originalLanguage converts TVDB's original-language code to BCP-47 at the
// provider boundary, where every other metadata client already speaks it.
//
// TVDB reports ISO 639-3 ("eng", "jpn") and this used to pass it through
// verbatim into Series.status.metadata.originalLanguage, a field every
// consumer reads as BCP-47 ("en", "ja"). pkg/decision failed open on the
// mismatch, so nothing was wrongly rejected -- but every TVDB-sourced series
// logged a warning per evaluation and its language conditions were inert.
// A code pkg/lang cannot normalise is passed through unchanged rather than
// dropped, so downstream keeps its existing fail-open handling of the unknown.
func originalLanguage(code string) string {
	if tag, ok := lang.Normalize(code); ok {
		return string(tag)
	}
	return code
}
