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

package tmdb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

func TestMovieMapsTMDBFieldsIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/movie/27205", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.Movie(context.Background(), "27205", "US")
	require.NoError(t, err)

	require.Equal(t, "Inception", m.Title)
	require.EqualValues(t, 148, m.Runtime)
	require.Equal(t, "tt1375666", m.IDs[metadata.KeyIMDb])
	require.Equal(t, "27205", m.IDs[metadata.KeyTMDB])
	require.EqualValues(t, 837, m.Ratings["tmdb"].ValueCentis, "8.369 * 100, rounded")
	require.True(t, m.InCinemas.Equal(time.Date(2010, 7, 16, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, metadata.MovieStatusReleased, m.Status)
}

// TestRatingSourcesDeclaresTMDBForMovieOnly is spec §C.2's table: tmdb
// declares its own source for MediaKindMovie and nothing for any other
// kind (this client has no SeriesProvider, so it must never be asked to
// rate a series it cannot fetch).
func TestRatingSourcesDeclaresTMDBForMovieOnly(t *testing.T) {
	c, err := tmdb.New("test-key", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	require.Equal(t, []string{metadata.RatingSourceTMDB}, c.RatingSources(commonv1.MediaKindMovie))
	require.Empty(t, c.RatingSources(commonv1.MediaKindSeries))
	require.Empty(t, c.RatingSources(commonv1.MediaKindAlbum))
}

// TestRatingsReusesTheMovieFetch proves Ratings(MediaKindMovie, ...) reads
// through the same recorded fixture (test/data/metadata/tmdb/movie_27205.json)
// TestMovieMapsTMDBFieldsIntoTheNormalizedModel does, and returns exactly
// the "tmdb" entry Movie's own mapMovie already builds from vote_average
// and vote_count -- "the fetch it already performs", not a second endpoint.
func TestRatingsReusesTheMovieFetch(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/movie/27205", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	ratings, err := c.Ratings(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205"})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "one call, the same GetMovieDetails Movie() already makes")
	require.Len(t, ratings, 1)
	require.Equal(t, metadata.RatingSourceTMDB, ratings["tmdb"].Source)
	require.EqualValues(t, 837, ratings["tmdb"].ValueCentis)
	require.EqualValues(t, 36892, ratings["tmdb"].Votes)
}

// TestRatingsRejectsAnythingButMovie proves this client refuses to be
// asked for a series' or any other kind's ratings -- it has never fetched
// one, and RatingSources already told the gateway not to ask.
func TestRatingsRejectsAnythingButMovie(t *testing.T) {
	c, err := tmdb.New("test-key", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Ratings(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "1399"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

// TestRatingsRequiresATMDBID proves a Movie whose ExternalIDs carries no
// tmdb key (only imdb, say) gets ErrUnsupported rather than a guessed
// lookup -- this client's only ratings-keying is TMDB's own id.
func TestRatingsRequiresATMDBID(t *testing.T) {
	c, err := tmdb.New("test-key", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Ratings(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt1375666"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

// TestMovieMapsAlternativeTitles: Movie asks TMDB to append the movie's
// alternative_titles and files them into AlternateTitles -- the movie's own
// title and a second country's identical title dropped, each kept title
// with its country and type -- which is what the gateway writes into
// MovieMetadata.alternateTitles for title matching. The fixture's titles
// follow TMDB's documented shape (golang-tmdb's AlternativeTitle); they are
// not a recorded live response.
func TestMovieMapsAlternativeTitles(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	var appended string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appended = r.URL.Query().Get("append_to_response")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.Movie(context.Background(), "27205", "US")
	require.NoError(t, err)

	require.Contains(t, strings.Split(appended, ","), "alternative_titles")
	require.Equal(t, []metadata.AltTitle{
		{Title: "A Origem", Country: "BR"},
		{Title: "Origen", Country: "ES"},
		{Title: "Inception: Le Origini", Country: "IT"},
		{Title: "盗梦空间", Country: "CN"},
		{Title: "Начало", Country: "RU"},
	}, m.AlternateTitles)
}

func TestMovieMapsA404ToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status_code":34,"status_message":"The resource you requested could not be found."}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "999999999", "US")

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestMovieMapsA429WithRetryAfterToRateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status_code":25,"status_message":"Your request count is over the allowed limit."}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "27205", "US")

	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 5*time.Second, rl.RetryAfter)
}

func TestMovieRequestsTheRegionAwareLanguage(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	var gotLanguage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLanguage = r.URL.Query().Get("language")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "27205", "GB")
	require.NoError(t, err)

	require.Equal(t, "en-GB", gotLanguage, "region GB must produce language en-GB, not the en-US default")
}

func TestMovieDefaultsToEnUSLanguageWhenRegionIsEmpty(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	var gotLanguage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLanguage = r.URL.Query().Get("language")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "27205", "")
	require.NoError(t, err)

	require.Equal(t, "en-US", gotLanguage)
}

func TestFindMovieResolvesByIMDbID(t *testing.T) {
	findBody, err := os.ReadFile("../../../../test/data/metadata/tmdb/find_imdb_tt1375666.json")
	require.NoError(t, err)
	movieBody, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/find/tt1375666":
			require.Equal(t, "imdb_id", r.URL.Query().Get("external_source"))
			_, _ = w.Write(findBody)
		case "/movie/27205":
			_, _ = w.Write(movieBody)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.FindMovie(context.Background(), metadata.ExternalIDs{metadata.KeyIMDb: "tt1375666"})

	require.NoError(t, err)
	require.Equal(t, "Inception", m.Title)
	require.Equal(t, "27205", m.IDs[metadata.KeyTMDB])
}

func TestFindMovieWithNoResultsReturnsErrNotFound(t *testing.T) {
	findBody, err := os.ReadFile("../../../../test/data/metadata/tmdb/find_imdb_notfound.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/find/tt9999999", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(findBody)
	}))
	defer srv.Close()

	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.FindMovie(context.Background(), metadata.ExternalIDs{metadata.KeyIMDb: "tt9999999"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestFindMovieWithNoUsableIDReturnsErrUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no HTTP call expected: %s", r.URL.Path)
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.FindMovie(context.Background(), metadata.ExternalIDs{metadata.KeyMBArtist: "irrelevant"})

	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

func TestMovieRejectsMalformedResponseBodies(t *testing.T) {
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
			c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
			require.NoError(t, err)

			var m *metadata.Movie
			require.NotPanics(t, func() {
				m, err = c.Movie(context.Background(), "27205", "US")
			})

			require.Nil(t, m)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}

func TestMovieDerivesSecondaryYearFromAPriorYearPremiere(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_premiere_prior_year.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.Movie(context.Background(), "900001", "US")
	require.NoError(t, err)

	require.EqualValues(t, 2021, m.Year)
	require.EqualValues(t, 2020, m.SecondaryYear, "the Sundance premiere year differs from the release_date year")
}

func TestMovieHasNoSecondaryYearWhenThePremiereIsInTheSameYear(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.Movie(context.Background(), "27205", "US")
	require.NoError(t, err)
	require.Zero(t, m.SecondaryYear)
}

func TestSearchMoviesMapsHitsAndSendsTheYearFilter(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/search_movie_inception.json")
	require.NoError(t, err)
	var gotQuery, gotYear, gotAdult string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search/movie", r.URL.Path)
		gotQuery = r.URL.Query().Get("query")
		gotYear = r.URL.Query().Get("year")
		gotAdult = r.URL.Query().Get("include_adult")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	hits, err := c.SearchMovies(context.Background(), "Inception", 2010)
	require.NoError(t, err)

	require.Equal(t, "Inception", gotQuery)
	require.Equal(t, "2010", gotYear)
	require.Equal(t, "false", gotAdult)
	require.Equal(t, []metadata.MovieHit{
		{
			IDs:    metadata.ExternalIDs{metadata.KeyTMDB: "27205"},
			Title:  "Inception",
			Year:   2010,
			Poster: "https://image.tmdb.org/t/p/w500/oYuLEt3zVCKq57qu2F8dT7NIa6f.jpg",
		},
		{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "613092"}, Title: "Inception: The Cobol Job"},
	}, hits)
}

func TestSearchMoviesOmitsTheYearFilterWhenYearIsZero(t *testing.T) {
	var sawYear bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawYear = r.URL.Query()["year"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"results":[],"total_pages":0,"total_results":0}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	hits, err := c.SearchMovies(context.Background(), "Nothing Matches", 0)
	require.NoError(t, err)
	require.Empty(t, hits)
	require.False(t, sawYear)
}

func TestSearchMoviesMapsProviderErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.SearchMovies(context.Background(), "Inception", 0)
	require.ErrorIs(t, err, metadata.ErrAuth)
}

// TestTwoClientsKeepTheirOwnBaseURLs is the regression for golang-tmdb's
// process-global base URL: SetCustomBaseURL writes a package variable, so
// the second New used to redirect the first Client to the second server.
func TestTwoClientsKeepTheirOwnBaseURLs(t *testing.T) {
	serve := func(title string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/movie/1", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"title":"` + title + `","release_date":"2000-01-01"}`))
		}))
	}
	first, second := serve("from-first"), serve("from-second")
	defer first.Close()
	defer second.Close()

	c1, err := tmdb.New("k1", first.Client(), first.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	c2, err := tmdb.New("k2", second.Client(), second.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m1, err := c1.Movie(context.Background(), "1", "")
	require.NoError(t, err)
	m2, err := c2.Movie(context.Background(), "1", "")
	require.NoError(t, err)

	require.Equal(t, "from-first", m1.Title, "the first Client must not follow the second Client's base URL")
	require.Equal(t, "from-second", m2.Title)
}

func TestBaseURLWithAPathPrefixIsKept(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"title":"x"}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL+"/mirror/3/", metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "1", "")
	require.NoError(t, err)
	require.Equal(t, "/mirror/3/movie/1", gotPath)
}

func TestNewRejectsARelativeBaseURL(t *testing.T) {
	_, err := tmdb.New("test-key", nil, "tmdb-stub:8080", metadata.NewLimiter(rate.Inf, 1))
	require.Error(t, err)
}

func TestMovieRejectsAnOversizedBodyWithoutLeakingTheAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"title":"`))
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(metadata.MaxResponseBytes)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	c, err := tmdb.New("secret-key-123", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Movie(context.Background(), "1", "")

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
	require.NotContains(t, err.Error(), "secret-key-123", "golang-tmdb puts the key in the query string; the *url.Error text must not surface")
}

// TestATransportFailureAfterASuccessIsNotADecodeError: statusCapture used to
// keep the previous response's 200 across a failed round trip, so mapError
// read it and called a refused connection a decode failure.
func TestATransportFailureAfterASuccessIsNotADecodeError(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	c, err := tmdb.New("secret-key-123", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	_, err = c.Movie(context.Background(), "27205", "")
	require.NoError(t, err)

	srv.Close() // every later round trip is refused

	_, err = c.Movie(context.Background(), "27205", "")
	require.Error(t, err)
	require.NotErrorIs(t, err, metadata.ErrDecode)
	require.NotContains(t, err.Error(), "secret-key-123")
}

// withImages is the recorded movie fixture with TMDB's image paths set:
// the recording has them null, and the library page has nothing to show
// until the client maps them.
func withImages(t *testing.T, body []byte, poster, backdrop any) []byte {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	doc["poster_path"], doc["backdrop_path"] = poster, backdrop
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return out
}

// A movie's poster and backdrop reach the normalized model as Images --
// the poster at TMDB's w500 size, the backdrop at original -- so
// status.metadata.images has a poster for the library page to show; null
// paths yield no images rather than a URL with nothing after the size.
func TestMovieMapsPosterAndBackdropIntoImages(t *testing.T) {
	recorded, err := os.ReadFile("../../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	body := withImages(t, recorded, "/inception-poster.jpg", "/inception-backdrop.jpg")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	m, err := c.Movie(context.Background(), "27205", "US")
	require.NoError(t, err)
	require.Equal(t, []metadata.Image{
		{Type: metadata.ImageTypePoster, URL: "https://image.tmdb.org/t/p/w500/inception-poster.jpg"},
		{Type: metadata.ImageTypeFanart, URL: "https://image.tmdb.org/t/p/original/inception-backdrop.jpg"},
	}, m.Images)

	body = withImages(t, recorded, nil, nil)
	m, err = c.Movie(context.Background(), "27205", "US")
	require.NoError(t, err)
	require.Empty(t, m.Images, "null paths are no images")
}
