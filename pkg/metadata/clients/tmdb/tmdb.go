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

// Package tmdb wraps github.com/cyruzin/golang-tmdb as a
// metadata.MovieProvider.
//
// golang-tmdb has no per-call context.Context (docs/research/metadata.md
// §2.1): this client honours ctx at the rate-limiter wait
// (limiter.Wait(ctx) returns immediately on cancellation) and with an
// upfront ctx.Err() check, but the in-flight HTTP round-trip itself cannot
// be cancelled through ctx -- a known, accepted limit of the adopted
// library, not a gap in this package.
//
// golang-tmdb's returned error (a decoded tmdb.Error) carries TMDB's own
// body-level status_code (e.g. 34 "resource not found"), never the HTTP
// status code that produced it (go doc github.com/cyruzin/golang-tmdb
// confirms Client.decodeError discards *http.Response after reading the
// body). To map HTTP 404/401/403/429 onto metadata's sentinel errors, this
// client wraps the http.Client's Transport in statusCaptureTransport, which
// records the most recent response's status code and Retry-After header for
// the method that issued the request to read immediately after the call
// returns.
package tmdb

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	rawtmdb "github.com/cyruzin/golang-tmdb"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Client is a metadata.MovieProvider backed by TMDB.
type Client struct {
	raw     *rawtmdb.Client
	capture *statusCapture
	limiter *rate.Limiter
}

// New builds a Client. httpClient may be nil, in which case
// http.DefaultTransport is used underneath the status-capturing wrapper.
// baseURL overrides golang-tmdb's default API host -- tests pass an
// httptest.Server URL; production callers pass "".
func New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*Client, error) {
	raw, err := rawtmdb.Init(apiKey)
	if err != nil {
		return nil, fmt.Errorf("tmdb: init client: %w", err)
	}

	capture := &statusCapture{}
	base := http.DefaultTransport
	wrapped := http.Client{}
	if httpClient != nil {
		if httpClient.Transport != nil {
			base = httpClient.Transport
		}
		wrapped.Timeout = httpClient.Timeout
		wrapped.Jar = httpClient.Jar
		wrapped.CheckRedirect = httpClient.CheckRedirect
	}
	wrapped.Transport = &statusCaptureTransport{base: base, capture: capture}
	raw.SetClientConfig(wrapped)

	if baseURL != "" {
		raw.SetCustomBaseURL(baseURL)
	}

	return &Client{raw: raw, capture: capture, limiter: limiter}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "tmdb" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{
		LookupBy: []string{metadata.KeyTMDB},
		Search:   true,
	}
}

// Movie fetches a single movie's details, release dates and external ids
// from TMDB, and derives InCinemas/DigitalRelease/PhysicalRelease/Status
// for region via metadata.DeriveRegionalReleases and
// metadata.DeriveMovieStatus.
func (c *Client) Movie(ctx context.Context, tmdbID string, region string) (*metadata.Movie, error) {
	ctx, span := tracing.Start(ctx, "metadata.tmdb.Movie")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := ctx.Err(); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	id, err := strconv.Atoi(tmdbID)
	if err != nil {
		err = fmt.Errorf("tmdb: invalid movie id %q: %w", tmdbID, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	d, err := c.raw.GetMovieDetails(id, map[string]string{
		"append_to_response": "release_dates,external_ids",
	})
	if err != nil {
		mapped := c.mapError(err)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "tmdb: movie fetch failed", "tmdb_id", tmdbID, "error", mapped)
		return nil, mapped
	}

	m := mapMovie(d, region)
	logger.DebugContext(ctx, "tmdb: movie fetched", "tmdb_id", tmdbID, "title", m.Title)
	return m, nil
}

// FindMovie looks a movie up by an external id already known to be a TMDB
// id (TMDB's own find-by-external-id endpoint is not wired here since
// Movie already accepts a TMDB id directly); any other key is
// metadata.ErrUnsupported until a later task adds TMDB's /find endpoint.
func (c *Client) FindMovie(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Movie, error) {
	if tmdbID, ok := ids[metadata.KeyTMDB]; ok {
		return c.Movie(ctx, tmdbID, "")
	}
	return nil, metadata.ErrUnsupported
}

// SearchMovies is not implemented by this task; it returns
// metadata.ErrUnsupported until a later task needs it.
func (c *Client) SearchMovies(context.Context, string, int) ([]metadata.MovieHit, error) {
	return nil, metadata.ErrUnsupported
}

var _ metadata.MovieProvider = (*Client)(nil)

// mapMovie converts a raw TMDB MovieDetails (with the release_dates and
// external_ids appends requested by Movie) into the normalized model.
func mapMovie(d *rawtmdb.MovieDetails, region string) *metadata.Movie {
	ids := metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(d.ID, 10)}
	// The nil check on MovieExternalIDsAppend itself must stay: d.MovieExternalIDs
	// below is a promoted field reached through that embedded pointer, and
	// evaluating a promoted field through a nil embedded pointer panics.
	if d.MovieExternalIDsAppend != nil && d.MovieExternalIDs != nil && d.MovieExternalIDs.IMDbID != "" {
		ids[metadata.KeyIMDb] = d.MovieExternalIDs.IMDbID
	} else if d.IMDbID != "" {
		ids[metadata.KeyIMDb] = d.IMDbID
	}

	genres := make([]string, 0, len(d.Genres))
	for _, g := range d.Genres {
		genres = append(genres, g.Name)
	}

	releaseDates := mapReleaseDates(d)
	inCinemas, digital, physical := metadata.DeriveRegionalReleases(releaseDates, region)

	m := &metadata.Movie{
		IDs:              ids,
		Title:            d.Title,
		OriginalTitle:    d.OriginalTitle,
		OriginalLanguage: d.OriginalLanguage,
		Overview:         d.Overview,
		Runtime:          int32(d.Runtime),
		Genres:           genres,
		Ratings: metadata.Ratings{
			"tmdb": {
				Source:      "tmdb",
				ValueCentis: int32(math.Round(float64(d.VoteAverage) * 100)),
				Votes:       int32(d.VoteCount),
				Kind:        "user",
			},
		},
		ReleaseDates:    releaseDates,
		InCinemas:       inCinemas,
		DigitalRelease:  digital,
		PhysicalRelease: physical,
	}
	m.Status = metadata.DeriveMovieStatus(inCinemas, digital, physical, time.Now())

	if year, ok := parseYear(d.ReleaseDate); ok {
		m.Year = year
	}
	if d.BelongsToCollection.ID != 0 {
		m.Collection = &metadata.Collection{
			IDs:   metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(d.BelongsToCollection.ID, 10)},
			Title: d.BelongsToCollection.Name,
		}
	}

	return m
}

// mapReleaseDates flattens TMDB's append_to_response release_dates
// (grouped per-country) into the normalized model's flat []ReleaseDate.
func mapReleaseDates(d *rawtmdb.MovieDetails) []metadata.ReleaseDate {
	// The nil check on MovieReleaseDatesAppend itself must stay: d.ReleaseDates
	// below is a promoted field reached through that embedded pointer, and
	// evaluating a promoted field through a nil embedded pointer panics.
	if d.MovieReleaseDatesAppend == nil || d.ReleaseDates == nil {
		return nil
	}
	results := d.ReleaseDates.MovieReleaseDatesResults
	if results == nil {
		return nil
	}
	var out []metadata.ReleaseDate
	for _, country := range results.Results {
		for _, rd := range country.ReleaseDates {
			t, err := time.Parse(time.RFC3339, rd.ReleaseDate)
			if err != nil {
				continue
			}
			out = append(out, metadata.ReleaseDate{
				Country:       country.Iso3166_1,
				Type:          metadata.ReleaseType(rd.Type),
				Date:          t,
				Certification: rd.Certification,
				Note:          rd.Note,
			})
		}
	}
	return out
}

// parseYear extracts the year from a TMDB "YYYY-MM-DD" release_date.
func parseYear(s string) (int32, bool) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, false
	}
	return int32(t.Year()), true
}

// statusCapture holds the most recently observed HTTP response's status
// code and Retry-After header, written by statusCaptureTransport and read
// by mapError immediately after a golang-tmdb call returns an error. It is
// scoped to a single Client and its own http.Client, so a genuinely
// concurrent pair of requests on the same Client can race on which
// response's status mapError sees -- acceptable here because golang-tmdb's
// own error carries no HTTP status at all, and every call site consults
// the capture immediately after its own synchronous request returns.
type statusCapture struct {
	mu         sync.Mutex
	statusCode int
	retryAfter string
}

func (s *statusCapture) record(resp *http.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = resp.StatusCode
	s.retryAfter = resp.Header.Get("Retry-After")
}

func (s *statusCapture) snapshot() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCode, s.retryAfter
}

// statusCaptureTransport wraps an http.RoundTripper to record each
// response's status before returning it unmodified, working around
// golang-tmdb discarding the *http.Response once it has decoded the body
// (see the package doc).
type statusCaptureTransport struct {
	base    http.RoundTripper
	capture *statusCapture
}

func (t *statusCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	t.capture.record(resp)
	return resp, nil
}

// mapError maps the HTTP status statusCapture observed on the most recent
// request to metadata's sentinel errors, falling back to wrapping err
// verbatim when no status was captured (a transport-level failure that
// never reached decodeError).
func (c *Client) mapError(err error) error {
	status, retryAfter := c.capture.snapshot()
	switch status {
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return metadata.ErrAuth
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "tmdb", RetryAfter: parseRetryAfter(retryAfter)}
	default:
		return fmt.Errorf("tmdb: %w", err)
	}
}

// parseRetryAfter parses a Retry-After header value (seconds, per TMDB) into
// a duration, defaulting to zero when absent or malformed.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
