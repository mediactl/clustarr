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

// Package comicvine is an in-house metadata.ComicProvider for ComicVine
// (https://comicvine.gamespot.com/api), the primary comic/manga metadata
// source (docs/research/metadata.md §2.5). There is no adopted Go client
// module, so every request goes through net/http directly.
//
// This client maps HTTP status only (404, 429-with-Retry-After); it does
// not add body-level status_code handling for ComicVine's "200 OK with an
// error status_code" convention mentioned anecdotally in some community
// clients, since the research note backing this task did not independently
// verify that table. See the TODO on Volume.
package comicvine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://comicvine.gamespot.com/api"

// userAgent is sent on every request: ComicVine is reported (community,
// unverified) to block the Go standard library's default
// "Go-http-client/..." User-Agent outright, so any non-empty custom value
// is sufficient -- this client does not need to imitate a browser.
const userAgent = "Clustarr/0.1 (+https://github.com/mediactl/clustarr)"

// Client is a metadata.ComicProvider backed by ComicVine.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	limiter *rate.Limiter
}

// New builds a Client. httpClient may be nil, in which case
// http.DefaultClient is used. baseURL overrides the default ComicVine host
// -- tests pass an httptest.Server URL; production callers pass "".
func New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{http: hc, baseURL: baseURL, apiKey: apiKey, limiter: limiter}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "comicvine" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyComicVine}, Search: true}
}

// searchVolumesResponse is ComicVine's status-enveloped search response.
type searchVolumesResponse struct {
	Results []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		StartYear string `json:"start_year"`
		Image     struct {
			SuperURL string `json:"super_url"`
		} `json:"image"`
	} `json:"results"`
}

// SearchVolumes searches ComicVine's volume index by name.
func (c *Client) SearchVolumes(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.SearchVolumes")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw searchVolumesResponse
	path := fmt.Sprintf("/search/?api_key=%s&format=json&resources=volume&query=%s&field_list=id,name,start_year,image",
		c.apiKey, url.QueryEscape(q))
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: search failed", "query", q, "error", err)
		return nil, err
	}

	hits := make([]metadata.SearchHit, 0, len(raw.Results))
	for _, r := range raw.Results {
		hit := metadata.SearchHit{
			IDs:    metadata.ExternalIDs{metadata.KeyComicVine: strconv.FormatInt(r.ID, 10)},
			Title:  r.Name,
			Poster: r.Image.SuperURL,
		}
		if year, err := strconv.Atoi(r.StartYear); err == nil {
			hit.Year = int32(year)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// volumeResponse is ComicVine's status-enveloped single-volume response
// (docs/research/metadata.md §2.5: every result is wrapped in a status
// envelope with error/status_code/results).
type volumeResponse struct {
	Results struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		StartYear string `json:"start_year"`
		Publisher struct {
			Name string `json:"name"`
		} `json:"publisher"`
		CountOfIssues int32  `json:"count_of_issues"`
		Description   string `json:"description"`
	} `json:"results"`
}

// Volume fetches a single comic volume by its ComicVine id
// (ids[metadata.KeyComicVine], already Validate-checked to be "NNNN-NNNNN").
//
// TODO(scope: ComicVine body-level status_code): map ComicVine's own
// status_code field (distinct from the HTTP status) once that table is
// independently verified -- this task maps HTTP status only.
func (c *Client) Volume(ctx context.Context, ids metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.Volume")
	defer span.End()
	logger := logging.FromContext(ctx)

	id, ok := ids[metadata.KeyComicVine]
	if !ok {
		err := fmt.Errorf("comicvine: Volume requires %q in ExternalIDs", metadata.KeyComicVine)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw volumeResponse
	path := fmt.Sprintf("/volume/%s?api_key=%s&format=json&field_list=id,name,start_year,publisher,count_of_issues,description,site_detail_url", id, c.apiKey)
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: volume fetch failed", "id", id, "error", err)
		return nil, err
	}

	v := &metadata.ComicVolume{
		IDs:         metadata.ExternalIDs{metadata.KeyComicVine: id},
		Title:       raw.Results.Name,
		Description: raw.Results.Description,
		Publisher:   raw.Results.Publisher.Name,
		IssueCount:  raw.Results.CountOfIssues,
	}
	if year, err := strconv.Atoi(raw.Results.StartYear); err == nil {
		y := int32(year)
		v.StartYear = &y
	}

	logger.DebugContext(ctx, "comicvine: volume fetched", "id", id, "title", v.Title)
	return v, nil
}

// issuesResponse is ComicVine's status-enveloped issue-list response.
type issuesResponse struct {
	Results []struct {
		ID          int64  `json:"id"`
		IssueNumber string `json:"issue_number"`
		Name        string `json:"name"`
		CoverDate   string `json:"cover_date"`
		StoreDate   string `json:"store_date"`
		Image       struct {
			SuperURL string `json:"super_url"`
		} `json:"image"`
	} `json:"results"`
}

// Issues fetches the issues belonging to volumeID.
func (c *Client) Issues(ctx context.Context, volumeID string) ([]metadata.ComicIssue, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.Issues")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw issuesResponse
	path := fmt.Sprintf("/issues/?api_key=%s&format=json&filter=volume:%s&field_list=id,issue_number,name,cover_date,store_date,image", c.apiKey, volumeID)
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: issues fetch failed", "volume_id", volumeID, "error", err)
		return nil, err
	}

	issues := make([]metadata.ComicIssue, 0, len(raw.Results))
	for _, r := range raw.Results {
		issue := metadata.ComicIssue{
			IDs:    metadata.ExternalIDs{metadata.KeyComicVine: strconv.FormatInt(r.ID, 10)},
			Number: r.IssueNumber,
			Title:  r.Name,
		}
		if t, err := time.Parse("2006-01-02", r.CoverDate); err == nil {
			issue.CoverDate = &t
		}
		if t, err := time.Parse("2006-01-02", r.StoreDate); err == nil {
			issue.StoreDate = &t
		}
		if r.Image.SuperURL != "" {
			issue.Image = &metadata.Image{Type: metadata.ImageTypeThumb, URL: r.Image.SuperURL}
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

var _ metadata.ComicProvider = (*Client)(nil)

// doGet issues a GET request against path (relative to c.baseURL), waiting
// on the rate limiter and setting a non-default User-Agent (ComicVine is
// reported to block Go's default one), and maps the HTTP status onto
// metadata's sentinel errors.
func (c *Client) doGet(ctx context.Context, path string, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("comicvine: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("comicvine: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("comicvine: decode %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "comicvine", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("comicvine: unexpected status %d for %s", resp.StatusCode, path)
	}
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
