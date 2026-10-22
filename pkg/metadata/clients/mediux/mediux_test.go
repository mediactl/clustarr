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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
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
	c, err := mediux.New(mediux.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "shared-test-token",
		UserAgent: "clustarr/test", Limiter: rate.NewLimiter(rate.Inf, 1)})
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

func TestASetBecomesTheItemsOverridesAtTheChosenQuality(t *testing.T) {
	c, srv, _ := recorded(t)
	set, err := c.ShowSet(context.Background(), "41471")
	require.NoError(t, err)
	e := c.Overrides(set)
	require.Equal(t, "41471", e.ID)
	require.Len(t, e.Artwork, 2)
	require.Equal(t, catalogv1.ImageTypePoster, e.Artwork[0].Type)
	require.Equal(t, srv.URL+"/assets/ea39f814-5cc8-4f3b-91dc-891e4c681cf3?v=20261016195509&key=jpg", e.Artwork[0].URL,
		"the file's versioned src under the client's host, at optimized quality")
	require.Equal(t, catalogv1.ImageTypeFanart, e.Artwork[1].Type)
	require.Len(t, e.SeasonArtwork, 22, "two season posters and twenty titlecards")
	require.Equal(t, catalogv1.SeasonArtworkOverride{Season: 1, Type: catalogv1.ImageTypePoster, URL: e.SeasonArtwork[0].URL}, e.SeasonArtwork[0])
	one := int32(1)
	require.Equal(t, int32(1), e.SeasonArtwork[2].Season)
	require.Equal(t, &one, e.SeasonArtwork[2].Episode)
	require.Equal(t, catalogv1.ImageTypeScreenshot, e.SeasonArtwork[2].Type)
	require.Contains(t, e.SeasonArtwork[2].URL, "&key=jpg")
	last := e.SeasonArtwork[len(e.SeasonArtwork)-1]
	require.Equal(t, int32(2), last.Season)
	require.Equal(t, int32(10), *last.Episode, "seasons then episodes, in number order")

	orig := mediux.ImageURL("https://images.mediux.io", set.Poster[0], mediux.QualityOriginal)
	require.Equal(t, "https://images.mediux.io/assets/ea39f814-5cc8-4f3b-91dc-891e4c681cf3?v=20261016195509", orig, "the original has no key")
}

func TestChooseTakesTheMostCompleteSetThenTheMostPopular(t *testing.T) {
	c, _, _ := recorded(t)
	show, err := c.ShowSets(context.Background(), "105248")
	require.NoError(t, err)
	policy := mediux.Policy{Languages: []string{"English"}, Seasons: []int32{1, 2}, Episodes: map[int32]int32{1: 10, 2: 10}}
	ch := mediux.Choose(show.Sets, policy)
	require.NotNil(t, ch)
	require.Equal(t, "41471", ch.Set.ID, "sharivari's: both season posters and all twenty titlecards, though Ludacris's poster-only set is more popular")
	require.Contains(t, ch.Reason, "2 of 2 season posters, 20 titlecards")

	policy.PreferredCreators = []string{"RandalPink87"}
	ch = mediux.Choose(show.Sets, policy)
	require.Equal(t, "38985", ch.Set.ID, "a preferred creator's set wins")
	require.Contains(t, ch.Reason, "preferred creator RandalPink87")

	policy.PreferredCreators = nil
	policy.Seasons, policy.Episodes = nil, nil
	ch = mediux.Choose(show.Sets, policy)
	require.Equal(t, "41472", ch.Set.ID, "with no series to complete, the most popular poster")
	require.Nil(t, mediux.Choose(nil, policy))
}

func TestAFilmsSetsAndACollectionsSetsAreRead(t *testing.T) {
	c, _, _ := recorded(t)
	m, err := c.MovieSets(context.Background(), "328387")
	require.NoError(t, err)
	require.Equal(t, "Nerve", m.Title)
	require.NotEmpty(t, m.Sets)
	e := c.MovieOverrides(&m.Sets[0])
	require.Equal(t, catalogv1.ImageTypePoster, e.Artwork[0].Type)
	require.Contains(t, e.Artwork[0].URL, "/assets/e5d104de-4f53-49df-b984-07fc5d70eabb?v=")
	col, err := c.CollectionSets(context.Background(), "404609")
	require.NoError(t, err)
	require.Equal(t, "John Wick Collection", col.Name)
	require.NotEmpty(t, col.Sets)
	require.Equal(t, "Aloha_Alona", col.Sets[0].Creator.Username)
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
