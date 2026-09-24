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

package hardcover_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/hardcover"
)

// The fixtures are Hasura-shaped GraphQL responses built from the field
// tables in hardcover-docs and the values blampe/rreading-glasses' own
// Hardcover test uses for "Out of My Mind" (book 141397, author 51942,
// edition 30405274): the API sits behind a token and Cloudflare, so no
// live response could be captured.
const fixtures = "../../../../test/data/metadata/hardcover/"

var opName = regexp.MustCompile(`^query (\w+)`)

type call struct {
	op   string
	vars map[string]any
	auth string
}

// serve answers each GraphQL operation name with the fixture routes names.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		m := opName.FindStringSubmatch(body.Query)
		require.NotNil(t, m, "every query is a named operation: %s", body.Query)
		calls = append(calls, call{m[1], body.Variables, r.Header.Get("Authorization")})
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			body, _ := os.ReadFile(fixtures + "unauthorized.json")
			_, _ = w.Write(body)
			return
		}
		name, ok := routes[m[1]]
		require.True(t, ok, "unexpected operation %s", m[1])
		b, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newClient(t *testing.T, srv *httptest.Server, token string) *hardcover.Client {
	t.Helper()
	c, err := hardcover.New(hardcover.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)
	return c
}

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestSearchBooksKeepsHardcoversOrderAcrossTheTwoRequests(t *testing.T) {
	srv, calls := serve(t, map[string]string{"Search": "search_out_of_my_mind.json", "BooksByID": "books_by_id.json"})
	c := newClient(t, srv, "good")

	hits, err := c.SearchBooks(context.Background(), "out of my mind")

	require.NoError(t, err)
	require.Len(t, *calls, 2)
	require.Equal(t, "out of my mind", (*calls)[0].vars["query"])
	require.Equal(t, []any{float64(141397), float64(8675309), float64(2000001)}, (*calls)[1].vars["ids"], "ids arrive as numbers or numeric strings and are sent as Ints")
	require.Equal(t, []metadata.SearchHit{
		{IDs: metadata.ExternalIDs{extid.KeyHardcover: "141397"}, Title: "Out of My Mind", Year: 2010, Poster: "https://assets.hardcover.app/edition/30405274/d41534ce6075b53289d1c4d57a6dac34b974ce91.jpeg"},
		{IDs: metadata.ExternalIDs{extid.KeyHardcover: "2000001"}, Title: "Out of My Mind: The Next Chapter", Year: 2023},
	}, hits, "search order, not the books query's order; an id the books table lacks is dropped")
}

func TestBookMapsWorkEditionsAuthorsSeriesAndRating(t *testing.T) {
	srv, calls := serve(t, map[string]string{"BookByID": "book_141397.json"})
	c := newClient(t, srv, "Bearer good")

	b, err := c.Book(context.Background(), metadata.ExternalIDs{extid.KeyHardcover: "141397", metadata.KeyOpenLibraryWork: "OL1W"})

	require.NoError(t, err)
	require.Equal(t, float64(141397), (*calls)[0].vars["id"])
	require.Equal(t, "Bearer good", (*calls)[0].auth, "a token that already carries Bearer is not doubled")
	require.Equal(t, metadata.ExternalIDs{extid.KeyHardcover: "141397"}, b.IDs)
	require.Equal(t, "Out of My Mind", b.Title)
	require.Equal(t, date(2010, time.January, 1), b.FirstPublished)
	require.Equal(t, []string{"Fiction", "Young Adult"}, b.Genres)
	require.Equal(t, []string{"51942"}, b.AuthorIDs, "the narrator is a credit, not an author")
	require.Equal(t, []metadata.SeriesLink{{Series: "Out of My Mind", Position: "1", Primary: true}}, b.Series)
	require.Equal(t, metadata.Rating{Source: "hardcover", ValueCentis: 822, Votes: 63}, b.Ratings["hardcover"], "4.111/5 is 8.22/10")
	require.Len(t, b.Editions, 2)
	require.Equal(t, metadata.Edition{
		IDs:         metadata.ExternalIDs{extid.KeyHardcover: "30405274", metadata.KeyISBN13: "9781416971702"},
		Title:       "Out of My Mind",
		Language:    "eng",
		Publisher:   "Atheneum",
		Format:      "Hardcover",
		PageCount:   295,
		ReleaseDate: date(2010, time.January, 1),
		Images:      []metadata.Image{{Type: metadata.ImageTypePoster, URL: "https://assets.hardcover.app/edition/30405274/d41534ce6075b53289d1c4d57a6dac34b974ce91.jpeg"}},
	}, b.Editions[0])
	require.Equal(t, "B003E7EU2U", b.Editions[1].IDs[metadata.KeyASIN])
	require.Nil(t, b.Editions[1].ReleaseDate, "a BC date is no date, not a wrong one")
}

func TestBookByISBNGoesThroughTheEdition(t *testing.T) {
	srv, calls := serve(t, map[string]string{"BookByISBN": "book_by_isbn_9781416971702.json"})
	c := newClient(t, srv, "good")

	b, err := c.Book(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "9781416971702"})

	require.NoError(t, err)
	require.Equal(t, "9781416971702", (*calls)[0].vars["isbn"])
	require.Equal(t, "141397", b.IDs[extid.KeyHardcover])
	require.Nil(t, b.Ratings, "no ratings, no Rating")
}

func TestANullPrimaryKeyIsErrNotFound(t *testing.T) {
	srv, _ := serve(t, map[string]string{"BookByID": "book_null.json"})
	c := newClient(t, srv, "good")

	_, err := c.Book(context.Background(), metadata.ExternalIDs{extid.KeyHardcover: "1"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestIdsHardcoverCannotServeAreUnsupportedWithoutARequest(t *testing.T) {
	srv, calls := serve(t, nil)
	c := newClient(t, srv, "good")
	ctx := context.Background()

	_, err := c.Book(ctx, metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	require.NotErrorIs(t, err, metadata.ErrNotFound, "a Discard-worthy ErrNotFound joined with a sibling provider's transient failure would drop a retry")

	_, err = c.Author(ctx, metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL21594A"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)

	_, err = c.Books(ctx, "OL21594A")
	require.ErrorIs(t, err, hardcover.ErrInvalidID)
	require.ErrorIs(t, err, metadata.ErrUnsupported, "rpc.go lookupBooks hands every provider an Open Library author key")

	_, err = c.Edition(ctx, metadata.ExternalIDs{metadata.KeyOpenLibraryEdition: "OL7353617M"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)

	_, err = c.Book(ctx, metadata.ExternalIDs{extid.KeyHardcover: "12 OR 1=1"})
	require.ErrorIs(t, err, hardcover.ErrInvalidID)

	require.Empty(t, *calls)
}

func TestAuthorMapsBioDatesAndHeadshot(t *testing.T) {
	srv, _ := serve(t, map[string]string{"AuthorByID": "author_51942.json"})
	c := newClient(t, srv, "good")

	a, err := c.Author(context.Background(), metadata.ExternalIDs{extid.KeyHardcover: "51942"})

	require.NoError(t, err)
	require.Equal(t, "Sharon M. Draper", a.Name)
	require.Equal(t, date(1948, time.August, 21), a.Born)
	require.Nil(t, a.Died)
	require.Equal(t, []metadata.Image{{Type: metadata.ImageTypeHeadshot, URL: "https://assets.hardcover.app/books/97020/10748148-L.jpg"}}, a.Images)
}

func TestBooksKeepsAuthoredWorksOnceEach(t *testing.T) {
	srv, calls := serve(t, map[string]string{"AuthorBooks": "author_books_51942.json"})
	c := newClient(t, srv, "good")

	books, err := c.Books(context.Background(), "51942")

	require.NoError(t, err)
	require.Equal(t, float64(51942), (*calls)[0].vars["id"])
	titles := make([]string, 0, len(books))
	for _, b := range books {
		titles = append(titles, b.Title)
		require.Equal(t, []string{"51942"}, b.AuthorIDs)
	}
	require.Equal(t, []string{"Out of My Mind", "Stella by Starlight"}, titles, "the foreword credit and the duplicate are dropped")
}

func TestEditionByASIN(t *testing.T) {
	srv, calls := serve(t, map[string]string{"EditionByASIN": "edition_by_asin.json"})
	c := newClient(t, srv, "good")

	e, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyASIN: "B003E7EU2U"})

	require.NoError(t, err)
	require.Equal(t, "B003E7EU2U", (*calls)[0].vars["asin"])
	require.Equal(t, "Recorded Books", e.Publisher)
	require.Equal(t, date(2010, time.March, 9), e.ReleaseDate)
}

func TestAGraphQLErrorIsErrQuery(t *testing.T) {
	srv, _ := serve(t, map[string]string{"BookByID": "graphql_error.json"})
	c := newClient(t, srv, "good")

	_, err := c.Book(context.Background(), metadata.ExternalIDs{extid.KeyHardcover: "141397"})

	require.ErrorIs(t, err, hardcover.ErrQuery)
	require.Contains(t, err.Error(), "field 'nope' not found")
}

func TestARejectedTokenIsErrAuth(t *testing.T) {
	srv, _ := serve(t, nil)
	c := newClient(t, srv, "expired")

	require.ErrorIs(t, c.Ping(context.Background()), metadata.ErrAuth)
}

func TestNewRequiresAToken(t *testing.T) {
	_, err := hardcover.New(hardcover.Config{Token: "  "})
	require.ErrorIs(t, err, hardcover.ErrNoToken)
}
