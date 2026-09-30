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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

const fixtures = "../../../../test/data/metadata/openlibrary/"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	require.NoError(t, err)
	return b
}

func newClient(srv *httptest.Server) *openlibrary.Client {
	return openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1)).
		WithWikidataBaseURL(srv.URL)
}

// searchServer serves the recorded works search for Dostoevsky
// (OL22242A), recording the query it was asked.
func searchServer(t *testing.T, query *string) *httptest.Server {
	t.Helper()
	body := fixture(t, "search_works_author_OL22242A_en.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search.json", r.URL.Path)
		*query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// An author's works come from Open Library's search, ranked by edition
// count, each titled by its edition in the client's language: Dostoevsky's
// works are catalogued under their Russian titles.
func TestBooksListsAnAuthorsWorksUnderTheirEnglishTitles(t *testing.T) {
	var query string
	srv := searchServer(t, &query)
	defer srv.Close()

	books, err := newClient(srv).WithLanguage("en").Books(context.Background(), "OL22242A")

	require.NoError(t, err)
	for _, want := range []string{"q=author_key%3AOL22242A", "lang=en", "sort=editions", "limit=50", "editions.title"} {
		require.Contains(t, query, want)
	}
	require.Len(t, books, 8)
	b := books[0]
	require.Equal(t, "Crime and Punishment", b.Title, "catalogued as Преступление и наказание")
	require.Equal(t, "OL166894W", b.IDs[metadata.KeyOpenLibraryWork])
	require.Contains(t, b.AuthorIDs, "OL22242A")
	require.Contains(t, b.Languages, "en")
	require.NotEmpty(t, b.Subjects)
	require.NotNil(t, b.FirstPublished)
	require.Empty(t, b.Editions, "the listing carries languages, not editions")

	var titles []string
	for _, b := range books {
		titles = append(titles, b.Title)
	}
	require.Contains(t, titles, "The Brothers Karamazov")
	require.Contains(t, titles, "The Idiot")
}

// With no language configured the catalogued title stands, and the search
// is not asked for one.
func TestBooksWithoutALanguageKeepsTheCataloguedTitles(t *testing.T) {
	var query string
	srv := searchServer(t, &query)
	defer srv.Close()

	books, err := newClient(srv).Books(context.Background(), "OL22242A")

	require.NoError(t, err)
	require.NotContains(t, query, "lang=")
	require.Equal(t, "Преступление и наказание", books[0].Title)
}

// A work with no editions block or author keys keeps its own title and is
// credited to the listed author.
func TestBooksToleratesAWorkWithNoEditionsOrAuthors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"docs":[{"key":"/works/OL1W","title":"Odd"}]}`))
	}))
	defer srv.Close()

	books, err := newClient(srv).WithLanguage("en").Books(context.Background(), "OL22242A")

	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, "Odd", books[0].Title)
	require.Equal(t, []string{"OL22242A"}, books[0].AuthorIDs)
}

// authorServer serves Dostoevsky's author record and, unless wikidataDown,
// Wikidata's English label for Q991; wikidataAsked counts label requests.
func authorServer(t *testing.T, wikidataDown bool, wikidataAsked *int) *httptest.Server {
	t.Helper()
	author, labels := fixture(t, "author_OL22242A.json"), fixture(t, "wikidata_Q991_labels_en.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/authors/OL22242A.json":
			_, _ = w.Write(author)
		case "/w/api.php":
			*wikidataAsked++
			require.Equal(t, "Q991", r.URL.Query().Get("ids"))
			require.Equal(t, "en", r.URL.Query().Get("languages"))
			if wikidataDown {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(labels)
		default:
			http.NotFound(w, r)
		}
	}))
}

// The author is named by Wikidata's label in the client's language when
// the Open Library record links one: Open Library has "Fiódor Dostoievski".
func TestAuthorTakesTheWikidataLabelInTheClientsLanguage(t *testing.T) {
	var asked int
	srv := authorServer(t, false, &asked)
	defer srv.Close()

	a, err := newClient(srv).WithLanguage("en").Author(context.Background(),
		metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL22242A"})

	require.NoError(t, err)
	require.Equal(t, "Fyodor Dostoyevsky", a.Name)
	require.Equal(t, 1, asked)
}

// Wikidata failing never fails the author: the Open Library name stands.
func TestAuthorKeepsTheOpenLibraryNameWhenWikidataFails(t *testing.T) {
	var asked int
	srv := authorServer(t, true, &asked)
	defer srv.Close()

	a, err := newClient(srv).WithLanguage("en").Author(context.Background(),
		metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL22242A"})

	require.NoError(t, err)
	require.Equal(t, "Fiódor Dostoievski", a.Name)
	require.Equal(t, 1, asked)
}

func TestAuthorWithoutALanguageAsksWikidataNothing(t *testing.T) {
	var asked int
	srv := authorServer(t, false, &asked)
	defer srv.Close()

	a, err := newClient(srv).Author(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL22242A"})

	require.NoError(t, err)
	require.Equal(t, "Fiódor Dostoievski", a.Name)
	require.Zero(t, asked)
}

// bookServer serves one recorded work and its editions page.
func bookServer(t *testing.T, workID string) *httptest.Server {
	t.Helper()
	work, editions := fixture(t, "work_"+workID+".json"), fixture(t, "editions_"+workID+".json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/works/"+workID+".json":
			_, _ = w.Write(work)
		case strings.HasPrefix(r.URL.Path, "/works/"+workID+"/editions.json"):
			_, _ = w.Write(editions)
		default:
			http.NotFound(w, r)
		}
	}))
}

// A work is titled by its most common edition title in the client's
// language -- so a metadata refresh keeps the listing's English title --
// and keeps its own title when that is already one of them.
func TestBookTakesItsMostCommonEditionTitleInTheClientsLanguage(t *testing.T) {
	srv := bookServer(t, "OL166894W")
	defer srv.Close()
	ids := metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL166894W"}

	b, err := newClient(srv).WithLanguage("en").Book(context.Background(), ids)
	require.NoError(t, err)
	require.Equal(t, "Crime and Punishment", b.Title, "27 of its first 100 editions; catalogued as Преступление и наказание")

	b, err = newClient(srv).Book(context.Background(), ids)
	require.NoError(t, err)
	require.Equal(t, "Преступление и наказание", b.Title, "no language: the catalogued title")

	pp := bookServer(t, "OL138052W")
	defer pp.Close()
	b, err = newClient(pp).WithLanguage("en").Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})
	require.NoError(t, err)
	require.Equal(t, "Pride and Prejudice", b.Title)
}
