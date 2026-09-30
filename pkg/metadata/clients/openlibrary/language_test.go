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
	"unicode"

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
	for _, want := range []string{"q=author_key%3AOL22242A", "lang=en", "sort=editions", "limit=100", "editions.title"} {
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

// Open Library holds many works for one book -- The Idiot four times
// ("The Idiot", "Idiot", "the idiot", "The idiot (The Modern library of the
// world's best books)"), The Brothers Karamazov five -- and each would be a
// Book of its own. Books lists each once, as its most-published work (the
// search's sort), under that work's English title.
func TestBooksListsEachBookOnceAsItsMostPublishedWork(t *testing.T) {
	body := fixture(t, "search_works_author_OL22242A_en_100.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	books, err := newClient(srv).WithLanguage("en").Books(context.Background(), "OL22242A")
	require.NoError(t, err)

	byTitle := map[string][]string{}
	for _, b := range books {
		k := strings.ToLower(b.Title)
		byTitle[k] = append(byTitle[k], b.IDs[metadata.KeyOpenLibraryWork])
	}
	for title, work := range map[string]string{
		"the idiot":              "OL166925W",
		"crime and punishment":   "OL166894W",
		"the brothers karamazov": "OL10432709W",
		"poor folk":              "OL16444720W",
		"the possessed":          "OL166971W",
	} {
		require.Equalf(t, []string{work}, byTitle[title], "%q is one Book, its most-published work", title)
	}
	for _, b := range books {
		for _, dup := range []string{"idiot", "brothers karamazov", "double", "double annotated", "poor folk annotated", "grand inquisitor", "gambler", "possessed", "house of the dead"} {
			require.NotEqualf(t, dup, strings.ToLower(b.Title), "%s is a second work of a book already listed", b.IDs[metadata.KeyOpenLibraryWork])
		}
	}
	require.Less(t, len(books), 70, "100 works, far fewer books")

	// Nor as "Brothers Karamazov by Fyodor Dostoevsky", "Brothers
	// Karamazov / Fyodor Dostoevsky" or "The Brothers Karamazov Volume 1
	// [EasyRead Large Edition]"; nor Notes from the Underground twice.
	var karamazov, underground int
	for _, b := range books {
		title := strings.ToLower(b.Title)
		if strings.Contains(title, "karamazov") {
			karamazov++
		}
		if strings.HasPrefix(title, "notes from") {
			underground++
		}
	}
	require.Equal(t, 1, karamazov, "one Brothers Karamazov")
	require.Equal(t, 1, underground, "one Notes from Underground")

	// A work with no title in the reader's language -- only Cyrillic,
	// like the Karamazov volumes "Братья Карамазовы 1/2" -- is a
	// translation or a volume of a book listed already; an edition
	// mistagged English ("Der Idiot. Roman" on a work catalogued as "The
	// idiot") is still that work's book.
	for _, b := range books {
		require.Truef(t, hasLatinLetter(b.Title), "%s is listed under %q, no English title", b.IDs[metadata.KeyOpenLibraryWork], b.Title)
		require.NotEqual(t, "Der Idiot. Roman", b.Title)
	}
}

func hasLatinLetter(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// Collapsing never merges two books: a short "X by Y" title, a collection
// and its title story, and two parts' distinct titles all stay listed.
func TestBooksKeepsDistinctBooksWithSimilarTitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"docs":[
			{"key":"/works/OL1W","title":"Stand by Me","author_name":["Stephen King"]},
			{"key":"/works/OL2W","title":"Stand","author_name":["Stephen King"]},
			{"key":"/works/OL3W","title":"An Honest Thief","author_name":["Fyodor Dostoevsky"]},
			{"key":"/works/OL4W","title":"An Honest Thief, and Other Stories","author_name":["Fyodor Dostoevsky"]},
			{"key":"/works/OL5W","title":"Uncle's Dream","author_name":["Fyodor Dostoevsky"]},
			{"key":"/works/OL6W","title":"Uncle's Dream and the Permanent Husband","author_name":["Fyodor Dostoevsky"]}
		]}`))
	}))
	defer srv.Close()

	books, err := newClient(srv).WithLanguage("en").Books(context.Background(), "OL1A")

	require.NoError(t, err)
	require.Len(t, books, 6)
}

// An author's poster is the first of the record's photos, by cover id
// (Open Library marks a deleted photo -1); the record carries no other
// image, so an author without photos has none.
func TestAuthorPosterIsTheFirstPhoto(t *testing.T) {
	var asked int
	srv := authorServer(t, false, &asked)
	defer srv.Close()

	a, err := newClient(srv).Author(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL22242A"})

	require.NoError(t, err)
	require.Equal(t, []metadata.Image{{
		Type: metadata.ImageTypePoster,
		URL:  "https://covers.openlibrary.org/a/id/14356956-L.jpg",
	}}, a.Images)
}

func TestAuthorPosterSkipsDeletedPhotos(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []metadata.Image
	}{
		"deleted first": {
			`{"key":"/authors/OL1A","name":"A","photos":[-1,42]}`,
			[]metadata.Image{{Type: metadata.ImageTypePoster, URL: "https://covers.openlibrary.org/a/id/42-L.jpg"}},
		},
		"only deleted": {`{"key":"/authors/OL1A","name":"A","photos":[-1]}`, nil},
		"none":         {`{"key":"/authors/OL1A","name":"A"}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			a, err := newClient(srv).Author(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL1A"})

			require.NoError(t, err)
			require.Equal(t, tc.want, a.Images)
		})
	}
}

// A book's poster is its work's first cover, else the first cover among
// the editions fetched with it: the Pride and Prejudice fixture's work has
// no covers, its editions do. Before 2026-09-30 no Book had a cover.
func TestBookPosterIsTheWorksCoverElseAnEditions(t *testing.T) {
	for workID, want := range map[string]string{
		"OL166894W": "https://covers.openlibrary.org/b/id/9411873-L.jpg",
		"OL138052W": "https://covers.openlibrary.org/b/id/14568556-L.jpg",
	} {
		t.Run(workID, func(t *testing.T) {
			srv := bookServer(t, workID)
			defer srv.Close()

			b, err := newClient(srv).Book(context.Background(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: workID})

			require.NoError(t, err)
			require.Equal(t, []metadata.Image{{Type: metadata.ImageTypePoster, URL: want}}, b.Images)
		})
	}
}
