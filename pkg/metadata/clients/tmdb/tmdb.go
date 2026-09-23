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
//
// golang-tmdb also keeps its base URL in a package-level variable
// (`var baseURL` in its tmdb.go, which SetCustomBaseURL mutates), so two
// Clients built with different base URLs in one process used to share
// whichever was set last. This client never calls SetCustomBaseURL: a
// custom base URL is applied per Client by baseURLTransport, which rewrites
// each request golang-tmdb builds against its own default base onto this
// Client's. And golang-tmdb decodes straight off the socket, so the
// response-size cap (metadata.MaxResponseBytes) is enforced beneath it, by
// metadata.CappedTransport.
package tmdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// libraryBaseURL is golang-tmdb v1.9.4's own defaultBaseURL: every request
// the library builds starts with it, because this package never mutates the
// library's global (see the package doc). baseURLTransport rewrites from it;
// TestLibraryBaseURLMatchesGolangTMDB holds it to the library's value so an
// upgrade that changes the default fails a test instead of silently
// bypassing every custom base URL.
const libraryBaseURL = "https://api.themoviedb.org/3"

// posterBaseURL is TMDB's image CDN at the w500 size
// (docs/research/metadata.md §2.1: images at
// https://image.tmdb.org/t/p/{size}{file_path}); a search hit's poster is a
// thumbnail, so the largest standard poster size short of "original".
const posterBaseURL = "https://image.tmdb.org/t/p/w500"

// backdropBaseURL is the same CDN at its original size, for a backdrop,
// which is a wide image no standard poster size suits.
const backdropBaseURL = "https://image.tmdb.org/t/p/original"

// New builds a Client. httpClient may be nil, in which case
// http.DefaultTransport is used underneath the status-capturing wrapper.
// baseURL overrides TMDB's default API host for this Client only -- tests
// pass an httptest.Server URL, a MetadataProvider may name a mirror or
// stub; production callers against TMDB itself pass "".
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
	rt := metadata.CappedTransport(base, metadata.MaxResponseBytes)
	if baseURL != "" {
		target, err := url.Parse(baseURL)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return nil, fmt.Errorf("tmdb: base URL %q must be an absolute URL", baseURL)
		}
		rt = &baseURLTransport{base: rt, target: target}
	}
	wrapped.Transport = &statusCaptureTransport{base: rt, capture: capture}
	raw.SetClientConfig(wrapped)

	return &Client{raw: raw, capture: capture, limiter: limiter}, nil
}

// baseURLTransport points one Client at a custom base URL without touching
// golang-tmdb's process-global one. golang-tmdb builds every request as
// libraryBaseURL + "/<resource>..."; this rewrites the scheme and host to
// target's and replaces libraryBaseURL's path prefix ("/3") with target's
// path, which is exactly the URL SetCustomBaseURL(target) would have made
// the library build -- so a stub serving "/movie/603" behind
// "http://tmdb-stub:8080" keeps working unchanged. A request that is not
// under libraryBaseURL (a redirect the base URL itself issued, say) passes
// through untouched.
type baseURLTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *baseURLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	lib, _ := url.Parse(libraryBaseURL) // a constant; covered by TestLibraryBaseURLMatchesGolangTMDB
	if req.URL.Scheme != lib.Scheme || req.URL.Host != lib.Host || !strings.HasPrefix(req.URL.Path, lib.Path+"/") {
		return t.base.RoundTrip(req)
	}
	u := *t.target
	u.Path = strings.TrimSuffix(t.target.Path, "/") + strings.TrimPrefix(req.URL.Path, lib.Path)
	u.RawPath = ""
	u.RawQuery = req.URL.RawQuery
	u.Fragment = ""

	rewritten := req.Clone(req.Context())
	rewritten.URL = &u
	rewritten.Host = "" // let the Host header follow the rewritten URL
	return t.base.RoundTrip(rewritten)
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
		"language":           languageFor(region),
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

// FindMovie looks a movie up by external id. A TMDB id delegates to Movie
// directly; an IMDb or TVDB id goes through TMDB's /find/{external_id}
// endpoint (external_source=imdb_id or tvdb_id), taking the first
// movie_results entry and delegating to Movie for the full record. An
// ExternalIDs carrying none of tmdb/imdb/tvdb is metadata.ErrUnsupported --
// this client has no other crosswalk to try.
func (c *Client) FindMovie(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Movie, error) {
	if tmdbID, ok := ids[metadata.KeyTMDB]; ok {
		return c.Movie(ctx, tmdbID, "")
	}

	var externalID, source string
	switch {
	case ids[metadata.KeyIMDb] != "":
		externalID, source = ids[metadata.KeyIMDb], "imdb_id"
	case ids[metadata.KeyTVDB] != "":
		externalID, source = ids[metadata.KeyTVDB], "tvdb_id"
	default:
		return nil, metadata.ErrUnsupported
	}

	ctx, span := tracing.Start(ctx, "metadata.tmdb.FindMovie")
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

	found, err := c.raw.GetFindByID(externalID, map[string]string{"external_source": source})
	if err != nil {
		mapped := c.mapError(err)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "tmdb: find failed", "external_id", externalID, "source", source, "error", mapped)
		return nil, mapped
	}
	if len(found.MovieResults) == 0 {
		tracing.RecordError(span, metadata.ErrNotFound)
		return nil, metadata.ErrNotFound
	}

	tmdbID := strconv.FormatInt(found.MovieResults[0].ID, 10)
	return c.Movie(ctx, tmdbID, "")
}

// SearchMovies searches TMDB's movie index by title (GET /search/movie,
// docs/research/metadata.md §2.1), returning the first page of hits in
// TMDB's own relevance order. year, when positive, is sent as TMDB's "year"
// filter rather than "primary_release_year": "year" matches any of the
// film's release dates, so a title an import list dates by its festival
// premiere still finds the film TMDB dates by its general release (the same
// tolerance MovieMetadata.SecondaryYear gives release identity). Adult
// titles are excluded, as TMDB's own default does.
func (c *Client) SearchMovies(ctx context.Context, q string, year int) ([]metadata.MovieHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.tmdb.SearchMovies")
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

	opts := map[string]string{"language": defaultLanguage, "include_adult": "false"}
	if year > 0 {
		opts["year"] = strconv.Itoa(year)
	}
	res, err := c.raw.GetSearchMovies(q, opts)
	if err != nil {
		mapped := c.mapError(err)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "tmdb: movie search failed", "query", q, "year", year, "error", mapped)
		return nil, mapped
	}

	var hits []metadata.MovieHit
	if res.SearchMoviesResults != nil {
		hits = make([]metadata.MovieHit, 0, len(res.Results))
		for _, r := range res.Results {
			hit := metadata.MovieHit{
				IDs:   metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(r.ID, 10)},
				Title: r.Title,
			}
			if y, ok := parseYear(r.ReleaseDate); ok {
				hit.Year = y
			}
			if r.PosterPath != "" {
				hit.Poster = posterBaseURL + r.PosterPath
			}
			hits = append(hits, hit)
		}
	}
	logger.DebugContext(ctx, "tmdb: movie search", "query", q, "year", year, "hits", len(hits))
	return hits, nil
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
	m.SecondaryYear = metadata.DeriveSecondaryYear(releaseDates, m.Year)
	if d.BelongsToCollection.ID != 0 {
		m.Collection = &metadata.Collection{
			IDs:   metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(d.BelongsToCollection.ID, 10)},
			Title: d.BelongsToCollection.Name,
		}
	}
	// TMDB publishes image paths, not URLs (research note §2.1); the
	// poster is what the library page shows, the backdrop its fanart. A
	// null path is no image, never a URL with nothing after the size.
	if d.PosterPath != "" {
		m.Images = append(m.Images, metadata.Image{Type: metadata.ImageTypePoster, URL: posterBaseURL + d.PosterPath})
	}
	if d.BackdropPath != "" {
		m.Images = append(m.Images, metadata.Image{Type: metadata.ImageTypeFanart, URL: backdropBaseURL + d.BackdropPath})
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

// defaultLanguage is TMDB's own default locale, used whenever no region is
// given.
const defaultLanguage = "en-US"

// languageFor derives a TMDB "language" query value (the
// ISO-639-1-ISO-3166-1 form TMDB expects, e.g. "en-GB") from a region code.
// This client only ever requests English localisations -- there is no
// per-language configuration yet, only per-region -- so region "" or "US"
// both produce the same defaultLanguage, and any other two-letter region
// becomes "en-<REGION>".
func languageFor(region string) string {
	if region == "" {
		return defaultLanguage
	}
	return "en-" + region
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

func (s *statusCapture) recordNone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = 0
	s.retryAfter = ""
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
		// Record "no status" rather than leaving the previous request's
		// status in place: mapError would otherwise read a stale 200 and
		// report a transport failure as a decode failure.
		t.capture.recordNone()
		return resp, err
	}
	t.capture.record(resp)
	return resp, nil
}

// mapError maps the HTTP status statusCapture observed on the most recent
// request to metadata's sentinel errors. A captured 200 with a non-nil err
// means the HTTP round trip succeeded but golang-tmdb's own JSON decode of
// the body failed (verified by reading golang-tmdb's Client.get: it only
// calls decodeError on a non-200 status, and returns a bare
// fmt.Errorf("could not decode the data: %s", err) straight from
// json.Decode otherwise) -- that is wrapped as metadata.ErrDecode rather
// than leaking golang-tmdb's raw error text as this package's only signal.
// No status at all (0) means a transport-level failure that never reached
// either path; it falls back to wrapping err.
//
// A body over metadata.MaxResponseBytes fails the round trip inside
// metadata.CappedTransport, and golang-tmdb returns http.Client.Do's error
// unchanged, so it arrives here as a *url.Error still wrapping
// metadata.ErrResponseTooLarge. Every *url.Error is unwrapped to its Op and
// cause first because its message carries the full request URL, and
// golang-tmdb puts the API key in the query string -- an error string ends
// up in logs, span events and a MetadataResponse, none of which may carry a
// credential.
func (c *Client) mapError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	if errors.Is(err, metadata.ErrResponseTooLarge) {
		return fmt.Errorf("tmdb: %w", err)
	}
	status, retryAfter := c.capture.snapshot()
	switch status {
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return metadata.ErrAuth
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "tmdb", RetryAfter: parseRetryAfter(retryAfter)}
	case http.StatusOK:
		return fmt.Errorf("tmdb: %w: %w", metadata.ErrDecode, err)
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
