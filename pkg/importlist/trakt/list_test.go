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

package trakt_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
)

// farFuture is a token expiry far enough out that Fetch never mistakes a
// fresh token for one needing a proactive refresh.
func farFuture(t *testing.T) time.Time {
	t.Helper()
	return time.Now().Add(24 * time.Hour)
}

func TestFetchWatchlistMovies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/users/nbatkins/watchlist/movies", r.URL.Path)
		assert.Equal(t, "full", r.URL.Query().Get("extended"))
		assert.Equal(t, "2", r.Header.Get("trakt-api-version"))
		assert.Equal(t, "cid", r.Header.Get("trakt-api-key"))
		assert.Equal(t, "Bearer access-tok", r.Header.Get("Authorization"))
		_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/watchlist_movies.json"))
	}))
	defer srv.Close()

	store := importlist.NewMemoryTokenStore()
	require.NoError(t, store.Save(t.Context(), importlist.Token{AccessToken: "access-tok", ExpiresAt: farFuture(t)}))

	l, err := trakt.New("trakt-watchlist", commonv1.MediaKindMovie,
		importlist.TraktConfig{ListType: importlist.TraktListTypeWatchlist, Username: "nbatkins"},
		trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, store, nil, trakt.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Deadpool", items[0].Title)
	assert.Equal(t, int32(2016), items[0].Year)
	assert.Equal(t, "tt1431045", items[0].ExternalIDs.IMDb)
	assert.Equal(t, "293660", items[0].ExternalIDs.TMDB)
}

func TestFetchRefreshesTokenOnceOn401(t *testing.T) {
	gets, refreshes := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/nbatkins/watchlist/movies":
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/watchlist_movies.json"))
		case "/oauth/token":
			refreshes++
			_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/token_refresh.json"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := importlist.NewMemoryTokenStore()
	require.NoError(t, store.Save(t.Context(), importlist.Token{AccessToken: "stale", RefreshToken: "old-refresh", ExpiresAt: farFuture(t)}))

	l, err := trakt.New("trakt-watchlist", commonv1.MediaKindMovie,
		importlist.TraktConfig{ListType: importlist.TraktListTypeWatchlist, Username: "nbatkins"},
		trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, store, nil, trakt.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, 2, gets)
	assert.Equal(t, 1, refreshes)

	tok, _, _ := store.Load(t.Context())
	assert.NotEqual(t, "stale", tok.AccessToken)
}

func TestFetchWatchlistShows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/users/nbatkins/watchlist/shows", r.URL.Path)
		_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/watchlist_shows.json"))
	}))
	defer srv.Close()

	store := importlist.NewMemoryTokenStore()
	require.NoError(t, store.Save(t.Context(), importlist.Token{AccessToken: "access-tok", ExpiresAt: farFuture(t)}))

	l, err := trakt.New("trakt-watchlist-shows", commonv1.MediaKindSeries,
		importlist.TraktConfig{ListType: importlist.TraktListTypeWatchlist, Username: "nbatkins"},
		trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, store, nil, trakt.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Breaking Bad", items[0].Title)
	assert.Equal(t, int32(2008), items[0].Year)
	assert.Equal(t, "tt0903747", items[0].ExternalIDs.IMDb)
	assert.Equal(t, "81189", items[0].ExternalIDs.TVDB)
}
