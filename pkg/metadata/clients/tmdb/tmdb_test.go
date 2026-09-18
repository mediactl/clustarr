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
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

func TestMovieMapsTMDBFieldsIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/tmdb/movie_27205.json")
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
	body, err := os.ReadFile("../../../../testdata/metadata/tmdb/movie_27205.json")
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
	body, err := os.ReadFile("../../../../testdata/metadata/tmdb/movie_27205.json")
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
	findBody, err := os.ReadFile("../../../../testdata/metadata/tmdb/find_imdb_tt1375666.json")
	require.NoError(t, err)
	movieBody, err := os.ReadFile("../../../../testdata/metadata/tmdb/movie_27205.json")
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
	findBody, err := os.ReadFile("../../../../testdata/metadata/tmdb/find_imdb_notfound.json")
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
