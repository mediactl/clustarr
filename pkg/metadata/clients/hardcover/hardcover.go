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

// Package hardcover is a metadata.BookProvider for Hardcover
// (https://api.hardcover.app/v1/graphql), the Goodreads replacement
// docs/research/metadata.md §2.4 names as the secondary book source.
//
// The API is Hasura GraphQL behind a personal access token sent as
// "authorization: Bearer <token>" (hardcover-docs, api/Getting-Started.mdx;
// blampe/rreading-glasses sends the same). Every query shape here is one of
// two verified sources: the field tables in hardcover-docs
// (api/GraphQL/Schemas/{Books,Editions,Authors,Contributions,Images}.mdx)
// and rreading-glasses' production queries (hardcover/queries.graphql:
// search{ids}, books_by_pk, editions_by_pk, authors_by_pk{contributions},
// cached_image(path: "url"), cached_tags(path: "$.Genre"),
// book_series{position series{id name}}). Hardcover allows at most one
// search per request (Getting-Started.mdx, "Rate Limits"), so SearchBooks
// is two requests: the search for ids, then the books.
//
// Hardcover keys everything by its own integer ids. A Book or Author CR is
// keyed by an Open Library id (design §4.2), which Hardcover cannot look
// up, so Hardcover serves those only once something has put a hardcover id
// or an ISBN-13 in their ExternalIDs; until then every call answers
// ErrUnsupported without a request, which the Registry's first-success
// loop steps past to the next provider.
package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is Hardcover's GraphQL endpoint.
const DefaultBaseURL = "https://api.hardcover.app/v1/graphql"

// DefaultRate and DefaultBurst are Hardcover's documented Free-plan limits
// (Getting-Started.mdx, "Rate Limits": 60 per minute, burst 10, 5,000 per
// day). The daily cap is not modelled here; spec.rateLimit.perDay is the
// place for it.
const (
	DefaultRate  rate.Limit = 1
	DefaultBurst int        = 10
)

// Sentinel errors.
var (
	// ErrNoToken is returned by New without a token: every Hardcover query
	// needs one.
	ErrNoToken = errors.New("hardcover: an access token is required")
	// ErrInvalidID is returned, before any request, for a hardcover id that
	// is not a positive integer -- an Open Library "OL…A" id handed to
	// Books included, which must never be read as a Hardcover one.
	ErrInvalidID = errors.New("hardcover: invalid id")
	// ErrQuery is a GraphQL-level failure: Hasura answers a query it
	// rejects with HTTP 200 and an "errors" array.
	ErrQuery = errors.New("hardcover: query failed")
)

var positiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// Token is the personal access token, with or without its "Bearer "
	// prefix.
	Token string
}

// Client is a metadata.BookProvider backed by Hardcover.
type Client struct {
	h        *httpjson.Client
	endpoint string
	auth     string
}

// New builds a Client, refusing a Config without a token.
func New(cfg Config) (*Client, error) {
	tok := strings.TrimSpace(cfg.Token)
	if tok == "" {
		return nil, ErrNoToken
	}
	if !strings.HasPrefix(strings.ToLower(tok), "bearer ") {
		tok = "Bearer " + tok
	}
	endpoint := cfg.BaseURL
	if endpoint == "" {
		endpoint = DefaultBaseURL
	}
	return &Client{
		h:        &httpjson.Client{Provider: "hardcover", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		endpoint: endpoint,
		auth:     tok,
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "hardcover" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{extid.KeyHardcover, metadata.KeyISBN13, metadata.KeyASIN}, Search: true}
}

// query sends one GraphQL operation and decodes its data into out.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	h := http.Header{"Authorization": {c.auth}}
	if err := c.h.PostJSON(ctx, c.endpoint, h, map[string]any{"query": q, "variables": vars}, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%w: %s", ErrQuery, strings.Join(msgs, "; "))
	}
	return c.h.Decode(env.Data, out)
}

// flexInt accepts a JSON number or a numeric string: search's ids are
// integers in rreading-glasses' generated client but a Typesense document
// id is a string, and a client that fails on either would fail on a
// schema tweak.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("hardcover: id %s: %w", b, err)
	}
	*f = flexInt(n)
	return nil
}

// jsonString reads a jsonb value selected with a path -- cached_image(path:
// "url") is a JSON string, or null.
type jsonString string

func (s *jsonString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = jsonString(v)
	return nil
}

const searchQuery = `query Search($query: String!) {
  search(query: $query, query_type: "book", per_page: 25, page: 1) { ids }
}`

const booksByIDQuery = `query BooksByID($ids: [Int!]!) {
  books(where: {id: {_in: $ids}}) { id title release_year cached_image(path: "url") }
}`

// SearchBooks searches Hardcover's book index, returning hits in
// Hardcover's relevance order.
func (c *Client) SearchBooks(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.SearchBooks")
	defer span.End()

	var found struct {
		Search struct {
			IDs []flexInt `json:"ids"`
		} `json:"search"`
	}
	if err := c.query(ctx, searchQuery, map[string]any{"query": q}, &found); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if len(found.Search.IDs) == 0 {
		return []metadata.SearchHit{}, nil
	}

	var books struct {
		Books []struct {
			ID          int64      `json:"id"`
			Title       string     `json:"title"`
			ReleaseYear *int32     `json:"release_year"`
			Image       jsonString `json:"cached_image"`
		} `json:"books"`
	}
	ids := make([]int64, len(found.Search.IDs))
	for i, id := range found.Search.IDs {
		ids[i] = int64(id)
	}
	if err := c.query(ctx, booksByIDQuery, map[string]any{"ids": ids}, &books); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	byID := make(map[int64]int, len(books.Books))
	for i, b := range books.Books {
		byID[b.ID] = i
	}
	hits := make([]metadata.SearchHit, 0, len(ids))
	for _, id := range ids {
		i, ok := byID[id]
		if !ok {
			continue // a search hit the books table no longer has
		}
		b := books.Books[i]
		hit := metadata.SearchHit{IDs: metadata.ExternalIDs{extid.KeyHardcover: strconv.FormatInt(b.ID, 10)}, Title: b.Title, Poster: string(b.Image)}
		if b.ReleaseYear != nil {
			hit.Year = *b.ReleaseYear
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

const editionFields = `id title subtitle asin isbn_13 edition_format pages release_date
  language { code3 } publisher { name } cached_image(path: "url")`

const bookFields = `id title subtitle description release_date rating ratings_count
  cached_tags(path: "$.Genre")
  cached_image(path: "url")
  book_series { position series { id name } }
  contributions { contribution author { id name } }
  editions(order_by: {score: desc_nulls_last}, limit: 50) { ` + editionFields + ` }`

var (
	bookByIDQuery   = `query BookByID($id: Int!) { books_by_pk(id: $id) { ` + bookFields + ` } }`
	bookByISBNQuery = `query BookByISBN($isbn: String!) { editions(where: {isbn_13: {_eq: $isbn}}, limit: 1) { book { ` + bookFields + ` } } }`
)

type rawEdition struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Subtitle      string     `json:"subtitle"`
	ASIN          string     `json:"asin"`
	ISBN13        string     `json:"isbn_13"`
	EditionFormat string     `json:"edition_format"`
	Pages         *int32     `json:"pages"`
	ReleaseDate   string     `json:"release_date"`
	Image         jsonString `json:"cached_image"`
	Language      *struct {
		Code3 string `json:"code3"`
	} `json:"language"`
	Publisher *struct {
		Name string `json:"name"`
	} `json:"publisher"`
}

type rawBook struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle"`
	Description  string   `json:"description"`
	ReleaseDate  string   `json:"release_date"`
	Rating       *float64 `json:"rating"`
	RatingsCount int32    `json:"ratings_count"`
	Tags         []struct {
		Tag string `json:"tag"`
	} `json:"cached_tags"`
	Image      jsonString `json:"cached_image"`
	BookSeries []struct {
		Position *json.Number `json:"position"`
		Series   *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"series"`
	} `json:"book_series"`
	Contributions []struct {
		Contribution *string `json:"contribution"`
		Author       *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
	Editions []rawEdition `json:"editions"`
}

// Book fetches a work by ids[extid.KeyHardcover], or by
// ids[metadata.KeyISBN13] through the edition that carries it. Any other id
// is ErrUnsupported: Hardcover has no Open Library crosswalk this client
// could use without guessing.
func (c *Client) Book(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.Book")
	defer span.End()

	var raw *rawBook
	switch {
	case ids[extid.KeyHardcover] != "":
		id, err := parseID(ids[extid.KeyHardcover])
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		var out struct {
			Book *rawBook `json:"books_by_pk"`
		}
		if err := c.query(ctx, bookByIDQuery, map[string]any{"id": id}, &out); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		raw = out.Book
	case ids[metadata.KeyISBN13] != "":
		var out struct {
			Editions []struct {
				Book *rawBook `json:"book"`
			} `json:"editions"`
		}
		if err := c.query(ctx, bookByISBNQuery, map[string]any{"isbn": ids[metadata.KeyISBN13]}, &out); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		if len(out.Editions) > 0 {
			raw = out.Editions[0].Book
		}
	default:
		err := fmt.Errorf("hardcover: Book needs %q or %q in ExternalIDs: %w", extid.KeyHardcover, metadata.KeyISBN13, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	if raw == nil {
		// Hasura answers a missing primary key with null, not a 404.
		return nil, fmt.Errorf("hardcover: book %v: %w", ids, metadata.ErrNotFound)
	}
	return mapBook(raw), nil
}

func mapBook(raw *rawBook) *metadata.Book {
	b := &metadata.Book{
		IDs:            metadata.ExternalIDs{extid.KeyHardcover: strconv.FormatInt(raw.ID, 10)},
		Title:          raw.Title,
		Overview:       raw.Description,
		FirstPublished: parseDate(raw.ReleaseDate),
		Provenance:     []metadata.Provenance{{Provider: "hardcover", FetchedAt: time.Now().UTC()}},
	}
	for _, t := range raw.Tags {
		if t.Tag != "" {
			b.Genres = append(b.Genres, t.Tag)
		}
	}
	seen := map[int64]bool{}
	for _, ct := range raw.Contributions {
		// An empty or "Author" contribution is authorship; any other role
		// Contributions.mdx lists ("Narrator", "Translator", "Illustrator",
		// "Editor", ...) is a credit, not an author of the work.
		if ct.Author == nil || (ct.Contribution != nil && *ct.Contribution != "" && !strings.EqualFold(*ct.Contribution, "Author")) {
			continue
		}
		if !seen[ct.Author.ID] {
			seen[ct.Author.ID] = true
			b.AuthorIDs = append(b.AuthorIDs, strconv.FormatInt(ct.Author.ID, 10))
		}
	}
	for i, s := range raw.BookSeries {
		if s.Series == nil || s.Series.Name == "" {
			continue
		}
		link := metadata.SeriesLink{Series: s.Series.Name, Primary: i == 0}
		if s.Position != nil {
			link.Position = s.Position.String()
		}
		b.Series = append(b.Series, link)
	}
	if raw.Rating != nil && raw.RatingsCount > 0 {
		b.Ratings = metadata.Ratings{"hardcover": {
			Source: "hardcover",
			// Hardcover rates 0-5; metadata.Rating is /10 in hundredths.
			ValueCentis: int32(math.Round(*raw.Rating * 200)),
			Votes:       raw.RatingsCount,
		}}
	}
	for i := range raw.Editions {
		b.Editions = append(b.Editions, mapEdition(&raw.Editions[i]))
	}
	return b
}

func mapEdition(raw *rawEdition) metadata.Edition {
	e := metadata.Edition{
		IDs:         metadata.ExternalIDs{extid.KeyHardcover: strconv.FormatInt(raw.ID, 10)},
		Title:       raw.Title,
		Subtitle:    raw.Subtitle,
		Format:      raw.EditionFormat,
		ReleaseDate: parseDate(raw.ReleaseDate),
	}
	if raw.ISBN13 != "" {
		e.IDs[metadata.KeyISBN13] = raw.ISBN13
	}
	if raw.ASIN != "" {
		e.IDs[metadata.KeyASIN] = raw.ASIN
	}
	if raw.Pages != nil {
		e.PageCount = *raw.Pages
	}
	if raw.Language != nil {
		e.Language = raw.Language.Code3
	}
	if raw.Publisher != nil {
		e.Publisher = raw.Publisher.Name
	}
	if raw.Image != "" {
		e.Images = []metadata.Image{{Type: metadata.ImageTypePoster, URL: string(raw.Image)}}
	}
	return e
}

var (
	editionByIDQuery   = `query EditionByID($id: Int!) { editions_by_pk(id: $id) { ` + editionFields + ` } }`
	editionByISBNQuery = `query EditionByISBN($isbn: String!) { editions(where: {isbn_13: {_eq: $isbn}}, limit: 1) { ` + editionFields + ` } }`
	editionByASINQuery = `query EditionByASIN($asin: String!) { editions(where: {asin: {_eq: $asin}}, limit: 1) { ` + editionFields + ` } }`
)

// Edition fetches one edition by ids[extid.KeyHardcover] (an edition id),
// else ids[metadata.KeyISBN13], else ids[metadata.KeyASIN].
func (c *Client) Edition(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Edition, error) {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.Edition")
	defer span.End()

	var raw *rawEdition
	switch {
	case ids[extid.KeyHardcover] != "":
		id, err := parseID(ids[extid.KeyHardcover])
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		var out struct {
			Edition *rawEdition `json:"editions_by_pk"`
		}
		if err := c.query(ctx, editionByIDQuery, map[string]any{"id": id}, &out); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		raw = out.Edition
	case ids[metadata.KeyISBN13] != "", ids[metadata.KeyASIN] != "":
		q, vars := editionByISBNQuery, map[string]any{"isbn": ids[metadata.KeyISBN13]}
		if ids[metadata.KeyISBN13] == "" {
			q, vars = editionByASINQuery, map[string]any{"asin": ids[metadata.KeyASIN]}
		}
		var out struct {
			Editions []rawEdition `json:"editions"`
		}
		if err := c.query(ctx, q, vars, &out); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		if len(out.Editions) > 0 {
			raw = &out.Editions[0]
		}
	default:
		err := fmt.Errorf("hardcover: Edition needs %q, %q or %q in ExternalIDs: %w", extid.KeyHardcover, metadata.KeyISBN13, metadata.KeyASIN, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("hardcover: edition %v: %w", ids, metadata.ErrNotFound)
	}
	e := mapEdition(raw)
	return &e, nil
}

const authorByIDQuery = `query AuthorByID($id: Int!) {
  authors_by_pk(id: $id) { id name bio born_date death_date cached_image(path: "url") }
}`

// Author fetches an author by ids[extid.KeyHardcover]; any other id is
// ErrUnsupported.
func (c *Client) Author(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Author, error) {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.Author")
	defer span.End()

	if ids[extid.KeyHardcover] == "" {
		err := fmt.Errorf("hardcover: Author needs %q in ExternalIDs: %w", extid.KeyHardcover, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	id, err := parseID(ids[extid.KeyHardcover])
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var out struct {
		Author *struct {
			ID        int64      `json:"id"`
			Name      string     `json:"name"`
			Bio       string     `json:"bio"`
			BornDate  string     `json:"born_date"`
			DeathDate string     `json:"death_date"`
			Image     jsonString `json:"cached_image"`
		} `json:"authors_by_pk"`
	}
	if err := c.query(ctx, authorByIDQuery, map[string]any{"id": id}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Author == nil {
		return nil, fmt.Errorf("hardcover: author %d: %w", id, metadata.ErrNotFound)
	}
	a := &metadata.Author{
		IDs:        metadata.ExternalIDs{extid.KeyHardcover: strconv.FormatInt(out.Author.ID, 10)},
		Name:       out.Author.Name,
		Overview:   out.Author.Bio,
		Born:       parseDate(out.Author.BornDate),
		Died:       parseDate(out.Author.DeathDate),
		Provenance: []metadata.Provenance{{Provider: "hardcover", FetchedAt: time.Now().UTC()}},
	}
	if out.Author.Image != "" {
		a.Images = []metadata.Image{{Type: metadata.ImageTypeHeadshot, URL: string(out.Author.Image)}}
	}
	return a, nil
}

// authorBooksQuery mirrors rreading-glasses' GetAuthorEditions: an
// author's contributions to books (not editions) that are in good standing
// (book_status_id 1 = OK; Books.mdx), most-read first.
const authorBooksQuery = `query AuthorBooks($id: Int!) {
  authors_by_pk(id: $id) {
    contributions(
      limit: 100
      order_by: {book: {users_count: desc}}
      where: {contributable_type: {_eq: "Book"}, book: {book_status_id: {_eq: "1"}}}
    ) { contribution book { id title subtitle description release_date cached_image(path: "url") } }
  }
}`

// Books lists the works authorID wrote, most-read first, capped at 100.
// authorID must be a Hardcover author id: the gateway hands every
// BookProvider the same id (app/catalog/metadata/rpc.go lookupBooks passes
// an Open Library "OL…A" key), and a non-numeric id is refused rather
// than read as something it is not.
func (c *Client) Books(ctx context.Context, authorID string) ([]metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.Books")
	defer span.End()

	id, err := parseID(authorID)
	if err != nil {
		err = fmt.Errorf("%w: %w", err, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	var out struct {
		Author *struct {
			Contributions []struct {
				Contribution *string `json:"contribution"`
				Book         *struct {
					ID          int64      `json:"id"`
					Title       string     `json:"title"`
					Subtitle    string     `json:"subtitle"`
					Description string     `json:"description"`
					ReleaseDate string     `json:"release_date"`
					Image       jsonString `json:"cached_image"`
				} `json:"book"`
			} `json:"contributions"`
		} `json:"authors_by_pk"`
	}
	if err := c.query(ctx, authorBooksQuery, map[string]any{"id": id}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Author == nil {
		return nil, fmt.Errorf("hardcover: author %d: %w", id, metadata.ErrNotFound)
	}
	books := make([]metadata.Book, 0, len(out.Author.Contributions))
	seen := map[int64]bool{}
	for _, ct := range out.Author.Contributions {
		if ct.Book == nil || seen[ct.Book.ID] {
			continue
		}
		if ct.Contribution != nil && *ct.Contribution != "" && !strings.EqualFold(*ct.Contribution, "Author") {
			continue // translated, narrated, illustrated -- not written by
		}
		seen[ct.Book.ID] = true
		books = append(books, metadata.Book{
			IDs:            metadata.ExternalIDs{extid.KeyHardcover: strconv.FormatInt(ct.Book.ID, 10)},
			AuthorIDs:      []string{strconv.FormatInt(id, 10)},
			Title:          ct.Book.Title,
			Overview:       ct.Book.Description,
			FirstPublished: parseDate(ct.Book.ReleaseDate),
		})
	}
	logging.FromContext(ctx).DebugContext(ctx, "hardcover: author books", "author", id, "books", len(books))
	return books, nil
}

const pingQuery = `query Ping { search(query: "tolkien", query_type: "author", per_page: 1, page: 1) { ids } }`

// Ping runs one single-result author search: the cheapest query that
// proves the token is accepted.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.hardcover.Ping")
	defer span.End()
	var out json.RawMessage
	if err := c.query(ctx, pingQuery, map[string]any{}, &out); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

func parseID(s string) (int64, error) {
	if !positiveInt.MatchString(s) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrInvalidID, s, err)
	}
	return n, nil
}

// parseDate reads Hardcover's "YYYY-MM-DD" dates as UTC midnight. A BC
// date or a year past 9999 -- both occur in Hardcover's data, per
// rreading-glasses' hcReleaseDate -- is no date rather than a wrong one.
func parseDate(s string) *time.Time {
	if s == "" || strings.HasSuffix(s, "BC") {
		return nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Year() > 9999 {
		return nil
	}
	t = t.UTC()
	return &t
}

var _ metadata.BookProvider = (*Client)(nil)
