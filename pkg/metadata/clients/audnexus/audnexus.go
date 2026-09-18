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

// Package audnexus is an in-house metadata.AudiobookProvider for Audnexus
// (https://api.audnex.us), a self-hosted Audible metadata mirror
// (docs/research/metadata.md §2.6). There is no adopted Go client module,
// so every request goes through net/http directly.
package audnexus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://api.audnex.us"

// Client is a metadata.AudiobookProvider backed by Audnexus.
type Client struct {
	http    *http.Client
	baseURL string
	limiter *rate.Limiter
}

// New builds a Client. httpClient may be nil, in which case
// http.DefaultClient is used. baseURL overrides the default Audnexus host
// -- tests pass an httptest.Server URL; production callers pass "" for the
// public instance or their own self-hosted URL.
func New(httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{http: hc, baseURL: baseURL, limiter: limiter}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "audnexus" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyASIN}}
}

// audiobookResponse is Audnexus's book record.
type audiobookResponse struct {
	ASIN     string `json:"asin"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Authors  []struct {
		ASIN string `json:"asin"`
		Name string `json:"name"`
	} `json:"authors"`
	Narrators []struct {
		Name string `json:"name"`
	} `json:"narrators"`
	PublisherName    string `json:"publisherName"`
	ReleaseDate      string `json:"releaseDate"`
	RuntimeLengthMin int64  `json:"runtimeLengthMin"`
	Summary          string `json:"summary"`
	Description      string `json:"description"`
	Image            string `json:"image"`
	Genres           []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"genres"`
	SeriesPrimary *struct {
		Name     string `json:"name"`
		Position string `json:"position"`
	} `json:"seriesPrimary"`
	SeriesSecondary *struct {
		Name     string `json:"name"`
		Position string `json:"position"`
	} `json:"seriesSecondary"`
	Rating     string `json:"rating"`
	Language   string `json:"language"`
	FormatType string `json:"formatType"`
	IsAdult    bool   `json:"isAdult"`
	ISBN       string `json:"isbn"`
}

// Audiobook fetches a single audiobook by ASIN, scoped to region (an
// Audible marketplace code, e.g. "us", "uk", "de" -- the same title can
// have a different ASIN per marketplace, so region is part of the request,
// not just an annotation on the result).
func (c *Client) Audiobook(ctx context.Context, asin string, region string) (*metadata.Audiobook, error) {
	ctx, span := tracing.Start(ctx, "metadata.audnexus.Audiobook")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw audiobookResponse
	path := "/books/" + asin + "?region=" + region
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "audnexus: audiobook fetch failed", "asin", asin, "region", region, "error", err)
		return nil, err
	}

	a := mapAudiobook(&raw, region)
	logger.DebugContext(ctx, "audnexus: audiobook fetched", "asin", asin, "region", region, "title", a.Title)
	return a, nil
}

// chaptersResponse is Audnexus's chapter listing.
type chaptersResponse struct {
	Chapters []struct {
		Title         string `json:"title"`
		StartOffsetMs int64  `json:"startOffsetMs"`
		LengthMs      int64  `json:"lengthMs"`
	} `json:"chapters"`
}

// Chapters fetches a single audiobook's chapter listing.
func (c *Client) Chapters(ctx context.Context, asin string, region string) ([]metadata.Chapter, error) {
	ctx, span := tracing.Start(ctx, "metadata.audnexus.Chapters")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw chaptersResponse
	path := "/books/" + asin + "/chapters?region=" + region
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "audnexus: chapters fetch failed", "asin", asin, "region", region, "error", err)
		return nil, err
	}

	chapters := make([]metadata.Chapter, 0, len(raw.Chapters))
	for _, ch := range raw.Chapters {
		chapters = append(chapters, metadata.Chapter{
			Title:         ch.Title,
			StartOffsetMs: ch.StartOffsetMs,
			LengthMs:      ch.LengthMs,
		})
	}
	return chapters, nil
}

var _ metadata.AudiobookProvider = (*Client)(nil)

// mapAudiobook converts a raw Audnexus response into the normalized model.
func mapAudiobook(raw *audiobookResponse, region string) *metadata.Audiobook {
	ids := metadata.ExternalIDs{metadata.KeyASIN: raw.ASIN}
	if raw.ISBN != "" {
		ids[metadata.KeyISBN13] = raw.ISBN
	}

	authors := make([]metadata.NamedRef, 0, len(raw.Authors))
	for _, au := range raw.Authors {
		authors = append(authors, metadata.NamedRef{Name: au.Name, ASIN: au.ASIN})
	}
	narrators := make([]string, 0, len(raw.Narrators))
	for _, n := range raw.Narrators {
		narrators = append(narrators, n.Name)
	}
	genres := make([]string, 0, len(raw.Genres))
	for _, g := range raw.Genres {
		genres = append(genres, g.Name)
	}

	var series []metadata.SeriesLink
	if raw.SeriesPrimary != nil {
		series = append(series, metadata.SeriesLink{Series: raw.SeriesPrimary.Name, Position: raw.SeriesPrimary.Position, Primary: true})
	}
	if raw.SeriesSecondary != nil {
		series = append(series, metadata.SeriesLink{Series: raw.SeriesSecondary.Name, Position: raw.SeriesSecondary.Position})
	}

	a := &metadata.Audiobook{
		IDs:         ids,
		Region:      region,
		Title:       raw.Title,
		Subtitle:    raw.Subtitle,
		Authors:     authors,
		Narrators:   narrators,
		Publisher:   raw.PublisherName,
		Runtime:     time.Duration(raw.RuntimeLengthMin) * time.Minute,
		Summary:     raw.Summary,
		Description: raw.Description,
		Language:    raw.Language,
		Format:      raw.FormatType,
		Genres:      genres,
		Series:      series,
		IsAdult:     raw.IsAdult,
	}
	if raw.Image != "" {
		a.Image = &metadata.Image{Type: metadata.ImageTypePoster, URL: raw.Image}
	}
	if t, err := time.Parse(time.RFC3339, raw.ReleaseDate); err == nil {
		a.ReleaseDate = &t
	}
	// Audnexus returns its rating as a decimal string on a 0-5 scale, the
	// same convention as MusicBrainz; the parse happens once, here, and
	// nothing downstream ever sees a float64 (Rating.ValueCentis is int32,
	// per CLAUDE.md's no-float rule for anything telemetry-shaped).
	if v, err := strconv.ParseFloat(raw.Rating, 64); err == nil {
		a.Rating = &metadata.Rating{
			Source:      "audnexus",
			ValueCentis: int32(math.Round(v * 2 * 100)),
			Kind:        "user",
		}
	}
	return a
}

// doGet issues a GET request against path (relative to c.baseURL), waiting
// on the rate limiter, and maps the HTTP status onto metadata's sentinel
// errors.
func (c *Client) doGet(ctx context.Context, path string, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("audnexus: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("audnexus: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("audnexus: decode %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "audnexus", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("audnexus: unexpected status %d for %s", resp.StatusCode, path)
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
