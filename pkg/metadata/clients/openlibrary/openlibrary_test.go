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

package openlibrary_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

func TestEditionMapsAnISBNLookupIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/openlibrary/isbn_9780141439518.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/isbn/9780141439518.json", r.URL.Path)
		require.Contains(t, r.Header.Get("User-Agent"), "Clustarr/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	e, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})

	require.NoError(t, err)
	require.Equal(t, "Pride and Prejudice", e.Title)
	require.Equal(t, "Penguin Classics", e.Publisher)
	require.Equal(t, "9780141439518", e.IDs[metadata.KeyISBN13])
	require.Equal(t, "OL3355576M", e.IDs[metadata.KeyOpenLibraryEdition])
	require.Len(t, e.Images, 1)
	require.Equal(t, "https://covers.openlibrary.org/b/id/8739161-L.jpg", e.Images[0].URL)
}

func TestEditionMapsAMissingISBNToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "0000000000000"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestEditionRequiresAnISBN13(t *testing.T) {
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Edition(context.Background(), metadata.ExternalIDs{})

	require.Error(t, err)
}

// TestAuthorMapsAnOpenLibraryAuthorRecord exercises /authors/{OLID}.json,
// documented in docs/research/metadata.md §2.4.
func TestAuthorMapsAnOpenLibraryAuthorRecord(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/openlibrary/author_OL21594A.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/authors/OL21594A.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	a, err := c.Author(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL21594A"})

	require.NoError(t, err)
	require.Equal(t, "Jane Austen", a.Name)
	require.Equal(t, "OL21594A", a.IDs[metadata.KeyOpenLibraryAuthor])
	require.Contains(t, a.Overview, "English novelist")
	require.NotNil(t, a.Born)
	require.True(t, a.Born.Equal(time.Date(1775, 12, 16, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, a.Died)
	require.True(t, a.Died.Equal(time.Date(1817, 7, 18, 0, 0, 0, 0, time.UTC)))
}

// TestBooksListsAnAuthorsWorks exercises /authors/{OLID}/works.json,
// documented in docs/research/metadata.md §2.4. Each entry is a full work
// record, and is mapped as fully as Book maps one -- it used to yield ids
// and a title only.
func TestBooksListsAnAuthorsWorks(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/openlibrary/works_OL21594A.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/authors/OL21594A/works.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	books, err := c.Books(context.Background(), "OL21594A")

	require.NoError(t, err)
	require.Len(t, books, 1)
	b := books[0]
	require.Equal(t, "Pride and Prejudice", b.Title)
	require.Equal(t, "OL138052W", b.IDs[metadata.KeyOpenLibraryWork])
	require.Equal(t, []string{"OL21594A"}, b.AuthorIDs)
	require.Contains(t, b.Overview, "Elizabeth Bennet")
	require.Equal(t, []string{"Fiction", "England", "Social classes"}, b.Subjects)
	require.NotNil(t, b.FirstPublished)
	require.True(t, b.FirstPublished.Equal(time.Date(1813, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.Empty(t, b.Editions, "Books does not spend a request per work on editions")
}

func TestBooksToleratesALegacyAuthorShapeAndFallsBackToTheListedAuthor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[{"key":"/works/OL1W","title":"Odd","authors":[{"author":"/authors/OL9A"}]}]}`))
	}))
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	books, err := c.Books(context.Background(), "OL21594A")

	require.NoError(t, err, "one oddly-shaped author reference must not fail the whole listing")
	require.Len(t, books, 1)
	require.Equal(t, []string{"OL21594A"}, books[0].AuthorIDs)
}

// openLibraryServer serves the work and its editions fixture, recording
// the editions request's query.
func openLibraryServer(t *testing.T, work []byte, editionsQuery *string) *httptest.Server {
	t.Helper()
	editions, err := os.ReadFile("../../../../test/data/metadata/openlibrary/editions_OL138052W.json")
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/works/OL138052W.json":
			_, _ = w.Write(work)
		case "/works/OL138052W/editions.json":
			if editionsQuery != nil {
				*editionsQuery = r.URL.RawQuery
			}
			_, _ = w.Write(editions)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestBookMapsAWorkRecord exercises /works/{OLID}.json and
// /works/{OLID}/editions.json, documented in docs/research/metadata.md
// §2.4.
func TestBookMapsAWorkRecord(t *testing.T) {
	work, err := os.ReadFile("../../../../test/data/metadata/openlibrary/work_OL138052W.json")
	require.NoError(t, err)
	var editionsQuery string
	srv := openLibraryServer(t, work, &editionsQuery)
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})

	require.NoError(t, err)
	require.Equal(t, "Pride and Prejudice", b.Title)
	require.Contains(t, b.Overview, "Elizabeth Bennet")
	require.Equal(t, []string{"Fiction", "England", "Social classes"}, b.Subjects)
	require.Equal(t, "OL138052W", b.IDs[metadata.KeyOpenLibraryWork])
	require.Equal(t, []string{"OL21594A"}, b.AuthorIDs)
	require.Equal(t, "limit=100", editionsQuery, "one page, sized to BookMetadata.Editions' MaxItems")
}

// TestBookFillsEditionsAndFirstPublished is the regression for Book never
// filling Editions or FirstPublished, which made the metadata profile's
// SkipMissingDate and SkipMissingISBN documented no-ops.
func TestBookFillsEditionsAndFirstPublished(t *testing.T) {
	work, err := os.ReadFile("../../../../test/data/metadata/openlibrary/work_OL138052W.json")
	require.NoError(t, err)
	srv := openLibraryServer(t, work, nil)
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})
	require.NoError(t, err)

	require.NotNil(t, b.FirstPublished)
	require.True(t, b.FirstPublished.Equal(time.Date(1813, 1, 1, 0, 0, 0, 0, time.UTC)), "the work's own date, earlier than every edition")
	require.Len(t, b.Editions, 3)

	german := b.Editions[0]
	require.Equal(t, metadata.ExternalIDs{
		metadata.KeyOpenLibraryEdition: "OL50552339M",
		metadata.KeyISBN13:             "9783730606490",
	}, german.IDs)
	require.Equal(t, "Stolz und Vorurteil", german.Title)
	require.Equal(t, "de", german.Language, `"/languages/ger" normalized to BCP-47`)
	require.Equal(t, "Anaconda", german.Publisher)
	require.Equal(t, "gebundene Ausgabe", german.Format)
	require.EqualValues(t, 448, german.PageCount)
	require.True(t, german.ReleaseDate.Equal(time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)))

	fine := b.Editions[1]
	require.Equal(t, "B00BEW9MJS", fine.IDs[metadata.KeyASIN], "identifiers.amazon is the edition's ASIN")
	require.NotContains(t, fine.IDs, metadata.KeyISBN13)
	require.Equal(t, "en", fine.Language)
}

func TestBookTakesTheEarliestEditionWhenTheWorkHasNoDateOrALaterOne(t *testing.T) {
	for name, work := range map[string]string{
		"no work date":    `{"key":"/works/OL138052W","title":"Pride and Prejudice"}`,
		"later work date": `{"key":"/works/OL138052W","title":"Pride and Prejudice","first_publish_date":"1990"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := openLibraryServer(t, []byte(work), nil)
			defer srv.Close()
			c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

			b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})
			require.NoError(t, err)
			require.NotNil(t, b.FirstPublished)
			require.True(t, b.FirstPublished.Equal(time.Date(1946, 1, 1, 0, 0, 0, 0, time.UTC)), "got %v", b.FirstPublished)
		})
	}
}

func TestBookFailsWhenTheEditionsFetchFails(t *testing.T) {
	work, err := os.ReadFile("../../../../test/data/metadata/openlibrary/work_OL138052W.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/works/OL138052W.json" {
			_, _ = w.Write(work)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})

	require.Nil(t, b, "a work with no editions would read downstream as one that has none")
	require.ErrorIs(t, err, metadata.ErrRateLimited)
}

func TestEditionRejectsAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"`))
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(metadata.MaxResponseBytes)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
}

// TestSearchBooksMapsGeneralSearchResults exercises /search.json?q=,
// documented in docs/research/metadata.md §2.4.
func TestSearchBooksMapsGeneralSearchResults(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/openlibrary/search_pride_and_prejudice.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search.json", r.URL.Path)
		require.Equal(t, "pride and prejudice", r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	hits, err := c.SearchBooks(context.Background(), "pride and prejudice")

	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "Pride and Prejudice", hits[0].Title)
	require.EqualValues(t, 1813, hits[0].Year)
	require.Equal(t, "OL138052W", hits[0].IDs[metadata.KeyOpenLibraryWork])
	require.Equal(t, "https://covers.openlibrary.org/b/id/8739161-L.jpg", hits[0].Poster)
}

func TestEditionRejectsMalformedResponseBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"truncated JSON", `{"id": 1, "title": "Hea`},
		{"garbage bytes", "not json at all {{{"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

			var e *metadata.Edition
			var err error
			require.NotPanics(t, func() {
				e, err = c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})
			})

			require.Nil(t, e)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}
