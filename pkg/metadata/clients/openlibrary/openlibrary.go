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

	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://openlibrary.org"

// editionsLimit is how many editions Book asks /works/{id}/editions.json
// for: one page, sized to BookMetadata.Editions' own
// +kubebuilder:validation:MaxItems=100. A popular work has thousands of
// editions (Pride and Prejudice: over four thousand) and nothing downstream
// can hold more than this many, so paging further would be spend with no
// consumer.
const editionsLimit = 100

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

// Author fetches a single author by their Open Library id
// (ids[metadata.KeyOpenLibraryAuthor]).
func (c *Client) Author(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Author, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Author")
	defer span.End()
	logger := logging.FromContext(ctx)

	olid, ok := ids[metadata.KeyOpenLibraryAuthor]
	if !ok {
		err := fmt.Errorf("openlibrary: Author requires %q in ExternalIDs", metadata.KeyOpenLibraryAuthor)
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
		IDs:      metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: olid},
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

// workRecord is Open Library's work record, as returned by
// /works/{OLID}.json and as each entry of /authors/{OLID}/works.json (the
// latter is a list of the same records -- verified against the live API:
// both carry key, title, authors, description, subjects, covers and, on
// some works, first_publish_date).
type workRecord struct {
	Key              string          `json:"key"`
	Title            string          `json:"title"`
	Description      json.RawMessage `json:"description"`
	Subjects         []string        `json:"subjects"`
	FirstPublishDate string          `json:"first_publish_date"`
	// Authors is [{"author": {"key": "/authors/OL..."}, "type": {...}}].
	// Each author is kept raw and decoded leniently by authorIDs: one
	// oddly-shaped legacy record must not fail a whole works listing.
	Authors []struct {
		Author json.RawMessage `json:"author"`
	} `json:"authors"`
}

// authorIDs returns the Open Library author ids a work credits, in order.
func (w workRecord) authorIDs() []string {
	var ids []string
	for _, a := range w.Authors {
		var ref struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(a.Author, &ref); err != nil || ref.Key == "" {
			continue
		}
		ids = append(ids, strings.TrimPrefix(ref.Key, "/authors/"))
	}
	return ids
}

// mapWork converts a work record into the normalized Book, without
// editions. fallbackAuthor is credited when the record names no author of
// its own (an entry of an author's works listing is that author's by
// definition).
func mapWork(w workRecord, fallbackAuthor string) metadata.Book {
	b := metadata.Book{
		IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(w.Key, "/works/")},
		AuthorIDs: w.authorIDs(),
		Title:     w.Title,
		Overview:  decodeOpenLibraryText(w.Description),
		Subjects:  w.Subjects,
	}
	if len(b.AuthorIDs) == 0 && fallbackAuthor != "" {
		b.AuthorIDs = []string{fallbackAuthor}
	}
	if t, ok := parseLenientDate(w.FirstPublishDate); ok {
		b.FirstPublished = &t
	}
	return b
}

// Books lists the works credited to authorID (an Open Library author id):
// one page of /authors/{OLID}/works.json at Open Library's default page
// size, each mapped with the same fields Book maps from a work record --
// title, overview, subjects, authors and first-publication date. Editions
// are not fetched per work here (that is one more request per work); Book
// fetches them.
func (c *Client) Books(ctx context.Context, authorID string) ([]metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Books")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw struct {
		Entries []workRecord `json:"entries"`
	}
	if err := c.doGet(ctx, "/authors/"+authorID+"/works.json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: author works fetch failed", "olid", authorID, "error", err)
		return nil, err
	}

	books := make([]metadata.Book, 0, len(raw.Entries))
	for _, e := range raw.Entries {
		books = append(books, mapWork(e, authorID))
	}
	return books, nil
}

// Book fetches a single work (ids[metadata.KeyOpenLibraryWork]) and one page
// of its editions (/works/{OLID}/editions.json, editionsLimit of them).
//
// FirstPublished is the earliest date among the work's own
// first_publish_date and every fetched edition's publish_date. Open
// Library's work-level date is sparse and sometimes later than an edition
// it lists, and "first published" means the earliest known publication, so
// taking the minimum is the reading that never reports a date later than
// one the provider itself shows.
//
// A failed editions fetch fails the call rather than returning the work
// with no editions: an empty Editions would read downstream as "this work
// has no editions" -- the metadata profile's SkipMissingISBN would act on
// it -- when the truth is that the fetch did not complete.
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

	var raw workRecord
	if err := c.doGet(ctx, "/works/"+workID+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: work fetch failed", "olid", workID, "error", err)
		return nil, err
	}
	b := mapWork(raw, "")
	b.IDs = metadata.ExternalIDs{metadata.KeyOpenLibraryWork: workID}

	var eds struct {
		Entries []editionResponse `json:"entries"`
	}
	path := "/works/" + workID + "/editions.json?limit=" + strconv.Itoa(editionsLimit)
	if err := c.doGet(ctx, path, &eds); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: work editions fetch failed", "olid", workID, "error", err)
		return nil, err
	}
	for _, e := range eds.Entries {
		ed := mapEdition(e)
		if ed.ReleaseDate != nil && (b.FirstPublished == nil || ed.ReleaseDate.Before(*b.FirstPublished)) {
			t := *ed.ReleaseDate
			b.FirstPublished = &t
		}
		b.Editions = append(b.Editions, ed)
	}

	logger.DebugContext(ctx, "openlibrary: work fetched", "olid", workID, "title", b.Title, "editions", len(b.Editions))
	return &b, nil
}

// editionResponse is Open Library's edition record, as returned by
// /isbn/{isbn}.json, /books/{OLID}.json and each entry of
// /works/{OLID}/editions.json (field names verified against the live API).
type editionResponse struct {
	Key            string   `json:"key"`
	Title          string   `json:"title"`
	Subtitle       string   `json:"subtitle"`
	Publishers     []string `json:"publishers"`
	PublishDate    string   `json:"publish_date"`
	ISBN13         []string `json:"isbn_13"`
	Covers         []int64  `json:"covers"`
	NumberOfPages  int32    `json:"number_of_pages"`
	PhysicalFormat string   `json:"physical_format"`
	Languages      []struct {
		Key string `json:"key"` // "/languages/eng"
	} `json:"languages"`
	Identifiers struct {
		Amazon []string `json:"amazon"`
	} `json:"identifiers"`
}

// mapEdition converts an edition record into the normalized model. An id
// is only carried when it is well-formed (metadata.Validate): Open Library
// is user-edited, and a malformed ISBN or ASIN in the crosswalk is worse
// than none. Language is converted to BCP-47 at this boundary, as
// Edition.language's CRD field documents ("/languages/ger" -> "de"); a code
// pkg/lang cannot resolve is dropped rather than passed through in a
// vocabulary the field does not use.
func mapEdition(raw editionResponse) metadata.Edition {
	e := metadata.Edition{
		IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryEdition: strings.TrimPrefix(raw.Key, "/books/")},
		Title:     raw.Title,
		Subtitle:  raw.Subtitle,
		Format:    raw.PhysicalFormat,
		PageCount: raw.NumberOfPages,
	}
	if len(raw.ISBN13) > 0 && metadata.Validate(metadata.ExternalIDs{metadata.KeyISBN13: raw.ISBN13[0]}) == nil {
		e.IDs[metadata.KeyISBN13] = raw.ISBN13[0]
	}
	if len(raw.Identifiers.Amazon) > 0 && metadata.Validate(metadata.ExternalIDs{metadata.KeyASIN: raw.Identifiers.Amazon[0]}) == nil {
		e.IDs[metadata.KeyASIN] = raw.Identifiers.Amazon[0]
	}
	if len(raw.Publishers) > 0 {
		e.Publisher = raw.Publishers[0]
	}
	if len(raw.Languages) > 0 {
		if tag, ok := lang.Normalize(strings.TrimPrefix(raw.Languages[0].Key, "/languages/")); ok {
			e.Language = string(tag)
		}
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
	return e
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

	e := mapEdition(raw)
	// The ISBN asked for is the one this edition is known by, whichever of
	// its isbn_13 values Open Library happens to list first.
	e.IDs[metadata.KeyISBN13] = isbn

	logger.DebugContext(ctx, "openlibrary: edition fetched", "isbn", isbn, "title", e.Title)
	return &e, nil
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
// sentinel errors. A 200 body is read through metadata.DecodeJSON's cap;
// every other status is answered from the status alone, its body never
// read.
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
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := metadata.DecodeJSON(resp.Body, metadata.MaxResponseBytes, out); err != nil {
			return fmt.Errorf("openlibrary: %s: %w", path, err)
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
