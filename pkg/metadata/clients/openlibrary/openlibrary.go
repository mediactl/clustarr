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

// Package openlibrary is an in-house metadata.BookProvider for Open
// Library (https://openlibrary.org), the free, CC0, no-key book metadata
// source Readarr's own upstream (Goodreads) left behind
// (docs/research/metadata.md §2.4). There is no adopted Go client module,
// so every request goes through net/http directly.
package openlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://openlibrary.org"

// openLibraryAuthorKey is the ExternalIDs key this client reads/writes for
// an Open Library author id (an "OLxxxxxxA" key). It is not one of
// pkg/metadata's recognised keys (ids.go defines only KeyOpenLibraryWork and
// KeyOpenLibraryEdition) -- ExternalIDs is a plain map, and an unrecognised
// key round-trips through Validate unchecked, so this needs no change to
// the shared crosswalk.
const openLibraryAuthorKey = "olauthor"

// Client is a metadata.BookProvider backed by Open Library.
type Client struct {
	http      *http.Client
	baseURL   string
	userAgent string
	limiter   *rate.Limiter
}

// New builds a Client. userAgent must identify the application and a
// contact -- Open Library rate-limits an unidentified client to 1rps
// instead of 3rps (docs/research/metadata.md §2.4). httpClient may be nil,
// in which case http.DefaultClient is used. baseURL overrides Open
// Library's default host -- tests pass an httptest.Server URL; production
// callers pass "".
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{http: hc, baseURL: baseURL, userAgent: userAgent, limiter: limiter}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "openlibrary" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{
		LookupBy: []string{metadata.KeyISBN13, metadata.KeyOpenLibraryWork, metadata.KeyOpenLibraryEdition},
		Search:   true,
	}
}

// SearchBooks searches Open Library's general work/edition index.
func (c *Client) SearchBooks(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.SearchBooks")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw struct {
		Docs []struct {
			Key              string `json:"key"`
			Title            string `json:"title"`
			FirstPublishYear int32  `json:"first_publish_year"`
			CoverID          int64  `json:"cover_i"`
		} `json:"docs"`
	}
	path := "/search.json?q=" + url.QueryEscape(q) + "&fields=key,title,first_publish_year,cover_i"
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: search failed", "query", q, "error", err)
		return nil, err
	}

	hits := make([]metadata.SearchHit, 0, len(raw.Docs))
	for _, d := range raw.Docs {
		hit := metadata.SearchHit{
			IDs:   metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(d.Key, "/works/")},
			Title: d.Title,
			Year:  d.FirstPublishYear,
		}
		if d.CoverID != 0 {
			hit.Poster = coverURLByID(d.CoverID)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Author fetches a single author by their Open Library id (the openLibraryAuthorKey
// entry of ids).
func (c *Client) Author(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Author, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Author")
	defer span.End()
	logger := logging.FromContext(ctx)

	olid, ok := ids[openLibraryAuthorKey]
	if !ok {
		err := fmt.Errorf("openlibrary: Author requires %q in ExternalIDs", openLibraryAuthorKey)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw struct {
		Key       string          `json:"key"`
		Name      string          `json:"name"`
		Bio       json.RawMessage `json:"bio"`
		BirthDate string          `json:"birth_date"`
		DeathDate string          `json:"death_date"`
	}
	if err := c.doGet(ctx, "/authors/"+olid+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: author fetch failed", "olid", olid, "error", err)
		return nil, err
	}

	a := &metadata.Author{
		IDs:      metadata.ExternalIDs{openLibraryAuthorKey: olid},
		Name:     raw.Name,
		Overview: decodeOpenLibraryText(raw.Bio),
	}
	if t, ok := parseLenientDate(raw.BirthDate); ok {
		a.Born = &t
	}
	if t, ok := parseLenientDate(raw.DeathDate); ok {
		a.Died = &t
	}
	return a, nil
}

// Books lists the works credited to authorID (an Open Library author id).
func (c *Client) Books(ctx context.Context, authorID string) ([]metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Books")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw struct {
		Entries []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := c.doGet(ctx, "/authors/"+authorID+"/works.json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: author works fetch failed", "olid", authorID, "error", err)
		return nil, err
	}

	books := make([]metadata.Book, 0, len(raw.Entries))
	for _, e := range raw.Entries {
		books = append(books, metadata.Book{
			IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(e.Key, "/works/")},
			AuthorIDs: []string{authorID},
			Title:     e.Title,
		})
	}
	return books, nil
}

// Book fetches a single work (ids[metadata.KeyOpenLibraryWork]).
func (c *Client) Book(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Book")
	defer span.End()
	logger := logging.FromContext(ctx)

	workID, ok := ids[metadata.KeyOpenLibraryWork]
	if !ok {
		err := fmt.Errorf("openlibrary: Book requires %q in ExternalIDs", metadata.KeyOpenLibraryWork)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw struct {
		Key         string          `json:"key"`
		Title       string          `json:"title"`
		Description json.RawMessage `json:"description"`
		Subjects    []string        `json:"subjects"`
	}
	if err := c.doGet(ctx, "/works/"+workID+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: work fetch failed", "olid", workID, "error", err)
		return nil, err
	}

	return &metadata.Book{
		IDs:      metadata.ExternalIDs{metadata.KeyOpenLibraryWork: workID},
		Title:    raw.Title,
		Overview: decodeOpenLibraryText(raw.Description),
		Subjects: raw.Subjects,
	}, nil
}

// editionResponse is Open Library's edition record, as returned by both
// /isbn/{isbn}.json and /books/{OLID}.json.
type editionResponse struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Publishers  []string `json:"publishers"`
	PublishDate string   `json:"publish_date"`
	ISBN13      []string `json:"isbn_13"`
	Covers      []int64  `json:"covers"`
}

// Edition fetches a single edition by ISBN-13 (ids[metadata.KeyISBN13]).
func (c *Client) Edition(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Edition, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Edition")
	defer span.End()
	logger := logging.FromContext(ctx)

	isbn, ok := ids[metadata.KeyISBN13]
	if !ok {
		err := fmt.Errorf("openlibrary: Edition requires %q in ExternalIDs", metadata.KeyISBN13)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw editionResponse
	if err := c.doGet(ctx, "/isbn/"+isbn+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: edition fetch failed", "isbn", isbn, "error", err)
		return nil, err
	}

	e := &metadata.Edition{
		IDs:   metadata.ExternalIDs{metadata.KeyISBN13: isbn, metadata.KeyOpenLibraryEdition: strings.TrimPrefix(raw.Key, "/books/")},
		Title: raw.Title,
	}
	if len(raw.Publishers) > 0 {
		e.Publisher = raw.Publishers[0]
	}
	if len(raw.Covers) > 0 && raw.Covers[0] > 0 {
		e.Images = []metadata.Image{{Type: metadata.ImageTypePoster, URL: coverURLByID(raw.Covers[0])}}
	}
	// publish_date is free text ("2003", "March 2003", "2003-01-01", ...),
	// not a fixed format -- a date that does not parse leaves ReleaseDate
	// nil rather than erroring the whole call.
	if t, ok := parseLenientDate(raw.PublishDate); ok {
		e.ReleaseDate = &t
	}

	logger.DebugContext(ctx, "openlibrary: edition fetched", "isbn", isbn, "title", e.Title)
	return e, nil
}

var _ metadata.BookProvider = (*Client)(nil)

// coverURLByID builds a large cover image URL from Open Library's numeric
// cover id (the "covers" array on an edition or "cover_i" on a search hit).
func coverURLByID(id int64) string {
	return fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", id)
}

// openLibraryText is a value that Open Library sometimes returns as a bare
// string and sometimes as {"type": "/type/text", "value": "..."} -- most
// visibly on Description/Bio fields. decodeOpenLibraryText handles both
// shapes and returns "" for anything else (absent, null, or a shape this
// client does not recognise) rather than erroring the whole call over a
// display-only field.
func decodeOpenLibraryText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var wrapped struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return wrapped.Value
	}
	return ""
}

// parseLenientDate parses Open Library's free-text date fields
// (publish_date, birth_date, death_date), which mix "2003", "March 2003"
// and "2003-01-01" in the wild. It tries the formats this client has
// actually observed and reports ok=false rather than guessing further.
func parseLenientDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "January 2, 2006", "January 2006", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if year, err := strconv.Atoi(s); err == nil && year > 0 {
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}

// doGet issues a GET request against path (relative to c.baseURL), waiting
// on the rate limiter and setting the contact User-Agent Open Library
// requires for its 3rps tier, and maps the HTTP status onto metadata's
// sentinel errors.
func (c *Client) doGet(ctx context.Context, path string, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("openlibrary: build request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openlibrary: %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("openlibrary: decode %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "openlibrary", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("openlibrary: unexpected status %d for %s", resp.StatusCode, path)
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
