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

package mediux_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
)

var fixtures = filepath.Join("..", "..", "..", "..", "test", "data", "metadata", "mediux")

// recorded serves the responses recorded from images.mediux.io on
// 2026-10-22 with the shared testing token, by the query's shape and
// variables, and counts the requests.
func recorded(t *testing.T) (*mediux.Client, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/graphql", r.URL.Path)
		require.Equal(t, "Bearer shared-test-token", r.Header.Get("Authorization"))
		require.Equal(t, "clustarr/test", r.Header.Get("User-Agent"))
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		file := ""
		switch {
		case strings.Contains(req.Query, "shows(filter"):
			if req.Variables["tvdb"] == "384541" {
				file = "show_by_tvdb_384541.json"
			} else {
				_, _ = w.Write([]byte(`{"data":{"shows":[]}}`))
				return
			}
		case strings.Contains(req.Query, "shows_by_id"):
			if req.Variables["tmdb"] == "105248" {
				file = "show_sets_105248.json"
			} else {
				_, _ = w.Write([]byte(`{"data":{"shows_by_id":null}}`))
				return
			}
		case strings.Contains(req.Query, "show_sets_by_id"):
			file = "show_set_41471.json"
		case strings.Contains(req.Query, "movies_by_id") && strings.Contains(req.Query, "collection_set"):
			// Not recorded: AURA's shape, constructed (filmCollectionImages).
			_, _ = w.Write([]byte(filmCollectionImages))
			return
		case strings.Contains(req.Query, "movies_by_id"):
			file = "movie_sets_328387.json"
		case strings.Contains(req.Query, "collections_by_id"):
			file = "collection_sets_404609.json"
		case strings.Contains(req.Query, "content_ids"):
			file = "content_ids_sample.json"
		default:
			t.Errorf("unexpected query: %s", req.Query)
			return
		}
		b, err := os.ReadFile(filepath.Join(fixtures, file))
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := mediux.New(mediux.Config{
		HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "shared-test-token",
		UserAgent: "clustarr/test", Limiter: rate.NewLimiter(rate.Inf, 1),
	})
	require.NoError(t, err)
	return c, srv, &calls
}

func TestNewNeedsAToken(t *testing.T) {
	_, err := mediux.New(mediux.Config{})
	require.ErrorIs(t, err, mediux.ErrNoToken)
}

func TestATheTVDBIdMapsToTheShowsTMDBId(t *testing.T) {
	c, _, _ := recorded(t)
	id, err := c.ShowByTVDB(context.Background(), "384541")
	require.NoError(t, err)
	require.Equal(t, "105248", id, "Cyberpunk: Edgerunners")
	_, err = c.ShowByTVDB(context.Background(), "1")
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestAShowsSetsAreReadWithTheirSeasonsAndEpisodes(t *testing.T) {
	c, _, _ := recorded(t)
	show, err := c.ShowSets(context.Background(), "105248")
	require.NoError(t, err)
	require.Equal(t, "Cyberpunk: Edgerunners", show.Title)
	require.Equal(t, "384541", show.TVDBID)
	require.Len(t, show.Sets, 14)
	var sharivari *mediux.ShowSet
	for i := range show.Sets {
		if show.Sets[i].ID == "41471" {
			sharivari = &show.Sets[i]
		}
	}
	require.NotNil(t, sharivari)
	require.Equal(t, "sharivari", sharivari.Creator.Username)
	require.Len(t, sharivari.Poster, 1)
	require.Equal(t, "English", sharivari.Poster[0].Language)
	require.Len(t, sharivari.Backdrop, 1)
	require.Len(t, sharivari.SeasonPosters, 2)
	require.Len(t, sharivari.Titlecards, 20)
	require.ElementsMatch(t, []int32{1, 2}, []int32{sharivari.SeasonPosters[0].Season, sharivari.SeasonPosters[1].Season}, "a poster per season, in MediUX's order")
	_, err = c.ShowSets(context.Background(), "999999999")
	require.ErrorIs(t, err, metadata.ErrNotFound, "a show MediUX does not have is null")
}

// An image's URL is the file's versioned src under the client's host:
// optimized quality adds key=jpg, the original has none.
func TestAnImagesURLIsItsVersionedSrcAtTheChosenQuality(t *testing.T) {
	c, srv, _ := recorded(t)
	set, err := c.ShowSet(context.Background(), "41471")
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/assets/ea39f814-5cc8-4f3b-91dc-891e4c681cf3?v=20261016195509&key=jpg", c.ImageURL(set.Poster[0]))
	orig := mediux.ImageURL("https://images.mediux.io", set.Poster[0], mediux.QualityOriginal)
	require.Equal(t, "https://images.mediux.io/assets/ea39f814-5cc8-4f3b-91dc-891e4c681cf3?v=20261016195509", orig, "the original has no key")
	require.Equal(t, "https://mediux.io/show-set/41471", mediux.SetURL(mediux.SetShow, "41471"))
}

// filmCollectionImages is MovieSets' second answer as AURA's query shapes
// it -- a film's posters and backdrops, each with its collection set --
// constructed, not recorded: the shared token was gone by 2026-10-27.
const filmCollectionImages = `{"data":{"movies_by_id":{"id":"328387",
	"posters":[{"id":"cp","modified_on":"2026-01-01T00:00:00Z","src":"/cp?v=1","language":{"display_name":"English"},
		"collection_set":{"id":"c9","set_title":"Thrillers","user_created":{"username":"b"},"date_updated":"2026-01-01T00:00:00Z","popularity":2}}],
	"backdrops":[{"id":"cb","modified_on":"2026-01-01T00:00:00Z","src":"/cb?v=1","language":null,
		"collection_set":{"id":"c9","set_title":"Thrillers","user_created":{"username":"b"},"date_updated":"2026-01-01T00:00:00Z","popularity":2}}]}}}`

func TestAFilmsSetsAndACollectionsSetsAreRead(t *testing.T) {
	c, _, calls := recorded(t)
	m, err := c.MovieSets(context.Background(), "328387")
	require.NoError(t, err)
	require.Equal(t, "Nerve", m.Title)
	require.Len(t, m.Sets, 4)
	require.Equal(t, "e5d104de-4f53-49df-b984-07fc5d70eabb", m.Sets[0].Poster[0].ID)
	require.Equal(t, int32(2), calls.Load(), "the film's sets, then its collection-set images")
	require.Len(t, m.CollectionSets, 1, "the poster and backdrop of one collection set are one set")
	cs := m.CollectionSets[0]
	require.Equal(t, "c9", cs.ID)
	require.Equal(t, "b", cs.Creator.Username)
	require.Equal(t, 2, *cs.Popularity)
	require.Equal(t, "cp", cs.Poster[0].ID)
	require.Equal(t, "English", cs.Poster[0].Language)
	require.Equal(t, "cb", cs.Backdrop[0].ID)

	b, err := json.Marshal(m)
	require.NoError(t, err)
	var back mediux.Movie
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, *m, back, "the cache keeps a film's collection sets")

	col, err := c.CollectionSets(context.Background(), "404609")
	require.NoError(t, err)
	require.Equal(t, "John Wick Collection", col.Name)
	require.NotEmpty(t, col.Sets)
	require.Equal(t, "Aloha_Alona", col.Sets[0].Creator.Username)
}

// The collection-images query was never recorded live, so a GraphQL error
// from it alone leaves the film its own sets; a limit on it fails the
// lookup, which is retried, rather than caching the film without them.
func TestAFilmKeepsItsSetsWhenTheCollectionQueryIsRefused(t *testing.T) {
	movie, err := os.ReadFile(filepath.Join(fixtures, "movie_sets_328387.json"))
	require.NoError(t, err)
	// Directus answers a query it cannot validate with 400 and the errors.
	second := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field \"collection_set\" on type \"posters\".","extensions":{"code":"GRAPHQL_VALIDATION"}}]}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "collection_set") {
			second(w)
			return
		}
		_, _ = w.Write(movie)
	}))
	defer srv.Close()
	c, err := mediux.New(mediux.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "x", Limiter: rate.NewLimiter(rate.Inf, 1)})
	require.NoError(t, err)
	m, err := c.MovieSets(context.Background(), "328387")
	require.NoError(t, err)
	require.Len(t, m.Sets, 4)
	require.Empty(t, m.CollectionSets)

	second = func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}
	_, err = c.MovieSets(context.Background(), "328387")
	var rl *metadata.RateLimitedError
	require.True(t, errors.As(err, &rl), "%v", err)
}

func TestContentIDsAreByKind(t *testing.T) {
	c, _, _ := recorded(t)
	ids, err := c.ContentIDs(context.Background())
	require.NoError(t, err)
	require.True(t, ids.Movies["100"])
	require.True(t, ids.Shows["100010"])
	require.False(t, ids.Shows["100"])
}

// api.mediux.pro answers the shared token with a GraphQL error, not a
// status: it is ErrAuth all the same, so the provider reads Ready=False.
func TestAGraphQLCredentialsErrorIsErrAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Invalid user credentials.","extensions":{"code":"INVALID_CREDENTIALS"}}]}`))
	}))
	defer srv.Close()
	c, err := mediux.New(mediux.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "x", Limiter: rate.NewLimiter(rate.Inf, 1)})
	require.NoError(t, err)
	_, err = c.ShowByTVDB(context.Background(), "384541")
	require.ErrorIs(t, err, metadata.ErrAuth)
}

func TestA429IsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c, err := mediux.New(mediux.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "x", Limiter: rate.NewLimiter(rate.Inf, 1)})
	require.NoError(t, err)
	_, err = c.ContentIDs(context.Background())
	var rl *metadata.RateLimitedError
	require.True(t, errors.As(err, &rl))
	require.Equal(t, "mediux", rl.Provider)
}

// A set cached as JSON (mediuxcache) comes back whole: the language, the
// season posters and the titlecards survive the round trip, which the
// first cut's decoders -- reading the wire's nested shape from Go's flat
// encoding -- did not, so every cached set chose as poster-only.
func TestASetRoundTripsThroughJSON(t *testing.T) {
	c, _, _ := recorded(t)
	set, err := c.ShowSet(context.Background(), "41471")
	require.NoError(t, err)
	b, err := json.Marshal(set)
	require.NoError(t, err)
	var back mediux.ShowSet
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, *set, back)
	require.Equal(t, "English", back.Poster[0].Language)
	require.Len(t, back.SeasonPosters, 2)
	require.Len(t, back.Titlecards, 20)
	require.True(t, slices.ContainsFunc(back.Titlecards, func(c mediux.Titlecard) bool { return c.Season == 2 && c.Episode == 10 }), "S02E10's titlecard, season and episode intact")

	show, err := c.ShowSets(context.Background(), "105248")
	require.NoError(t, err)
	b, err = json.Marshal(show)
	require.NoError(t, err)
	var showBack mediux.Show
	require.NoError(t, json.Unmarshal(b, &showBack))
	require.Equal(t, *show, showBack)
}
