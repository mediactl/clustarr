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
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/isbn_9780141439518.json")
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
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/author_OL21594A.json")
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
// documented in docs/research/metadata.md §2.4.
func TestBooksListsAnAuthorsWorks(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/works_OL21594A.json")
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
	require.Equal(t, "Pride and Prejudice", books[0].Title)
	require.Equal(t, "OL138052W", books[0].IDs[metadata.KeyOpenLibraryWork])
	require.Equal(t, []string{"OL21594A"}, books[0].AuthorIDs)
}

// TestBookMapsAWorkRecord exercises /works/{OLID}.json, documented in
// docs/research/metadata.md §2.4.
func TestBookMapsAWorkRecord(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/work_OL138052W.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/works/OL138052W.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})

	require.NoError(t, err)
	require.Equal(t, "Pride and Prejudice", b.Title)
	require.Contains(t, b.Overview, "Elizabeth Bennet")
	require.Equal(t, []string{"Fiction", "England", "Social classes"}, b.Subjects)
	require.Equal(t, "OL138052W", b.IDs[metadata.KeyOpenLibraryWork])
}

// TestSearchBooksMapsGeneralSearchResults exercises /search.json?q=,
// documented in docs/research/metadata.md §2.4.
func TestSearchBooksMapsGeneralSearchResults(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/search_pride_and_prejudice.json")
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
