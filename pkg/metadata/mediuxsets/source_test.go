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

package mediuxsets_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
	"github.com/mediactl/clustarr/pkg/metadata/mediuxcache"
	"github.com/mediactl/clustarr/pkg/metadata/mediuxsets"
)

var fixtures = filepath.Join("..", "..", "..", "test", "data", "metadata", "mediux")

// recorded serves the responses recorded from images.mediux.io on
// 2026-10-22 (the mediux client's fixtures), counting the requests.
func recorded(t *testing.T) (*mediux.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		file := ""
		switch {
		case strings.Contains(req.Query, "shows(filter"):
			if req.Variables["tvdb"] != "384541" {
				_, _ = w.Write([]byte(`{"data":{"shows":[]}}`))
				return
			}
			file = "show_by_tvdb_384541.json"
		case strings.Contains(req.Query, "shows_by_id"):
			if req.Variables["tmdb"] != "105248" {
				_, _ = w.Write([]byte(`{"data":{"shows_by_id":null}}`))
				return
			}
			file = "show_sets_105248.json"
		case strings.Contains(req.Query, "show_sets_by_id"):
			file = "show_set_41471.json"
		case strings.Contains(req.Query, "movies_by_id") && strings.Contains(req.Query, "collection_set"):
			_, _ = w.Write([]byte(`{"data":{"movies_by_id":{"id":"328387","posters":[],"backdrops":[]}}}`))
			return
		case strings.Contains(req.Query, "movies_by_id"):
			file = "movie_sets_328387.json"
		case strings.Contains(req.Query, "content_ids"):
			// The recorded list is a sample; the cache consults it before
			// any set query, so it names the two items under test.
			_, _ = w.Write([]byte(`{"data":{"content_ids":[{"id":"105248","type":"show"},{"id":"328387","type":"movie"}]}}`))
			return
		default:
			t.Errorf("unexpected query: %s", req.Query)
			return
		}
		b, err := os.ReadFile(filepath.Join(fixtures, file))
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := mediux.New(mediux.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "t", Limiter: rate.NewLimiter(rate.Inf, 1)})
	require.NoError(t, err)
	return c, &calls
}

func TestASeriesIsComposedByItsTheTVDBIdThroughTheCache(t *testing.T) {
	c, calls := recorded(t)
	bus := membus.New(clockwork.NewFakeClockAt(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)))
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	src := &mediuxsets.Source{Client: c, Lookup: &mediuxcache.Cache{KV: bus.KV(events.BucketMediux), Lookup: c}, Policy: mediux.Policy{Languages: []string{"English"}}}
	q := metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries, TVDBID: "384541", Seasons: []int32{1, 2}, Episodes: map[int32]int32{1: 10, 2: 10}}
	set, err := src.ImageSet(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, "mediux", set.Provider)
	assert.Equal(t, "41471", set.SetID, "sharivari's complete set anchors")
	assert.Equal(t, "sharivari", set.Creator)
	assert.Equal(t, "https://mediux.io/show-set/41471", set.URL)
	assert.Contains(t, set.Reason, "2 of 2 season posters")
	assert.Len(t, set.Images, 24, "poster, backdrop, two season posters, twenty titlecards")
	assert.Equal(t, metadata.ImageSetImage{Type: "poster", Scope: metadata.ImageScopeItem, URL: set.Images[0].URL, SetID: "41471"}, set.Images[0])
	assert.Equal(t, metadata.ImageSetImage{Type: "fanart", Scope: metadata.ImageScopeItem, URL: set.Images[1].URL, SetID: "41471"}, set.Images[1])
	assert.Equal(t, metadata.ImageSetImage{Type: "poster", Scope: metadata.ImageScopeSeason, Season: 1, URL: set.Images[2].URL, SetID: "41471"}, set.Images[2])
	last := set.Images[23]
	assert.Equal(t, metadata.ImageSetImage{Type: "screenshot", Scope: metadata.ImageScopeEpisode, Season: 2, Episode: 10, URL: last.URL, SetID: "41471"}, last)
	assert.Equal(t, []metadata.ImageSetSource{{SetID: "41471", Title: set.Title, Creator: "sharivari", Kind: "show", URL: set.URL, Images: 24}}, set.Sets)
	assert.Equal(t, int32(3), calls.Load(), "the id mapping, the content list and the sets")

	again, err := src.ImageSet(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, set.SetID, again.SetID)
	assert.Equal(t, int32(3), calls.Load(), "every answer from the cache")

	artwork, seasons := mediuxsets.Overrides(set.Images)
	assert.Len(t, artwork, 2)
	assert.Len(t, seasons, 22)

	_, err = src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries, TVDBID: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound, "a show MediUX cannot map")
	_, err = src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries})
	require.ErrorIs(t, err, metadata.ErrNotFound, "no id at all")
}

// A preferred creator's set anchors and the others fill what it lacks; the
// answer names every set that supplied an image.
func TestASeriesTakesWhatItsAnchorLacksFromOtherSets(t *testing.T) {
	c, _ := recorded(t)
	src := &mediuxsets.Source{Client: c, Lookup: mediuxsets.Direct{Client: c}, Policy: mediux.Policy{Languages: []string{"English"}, PreferredCreators: []string{"Praythea"}}}
	set, err := src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries, TMDBID: "105248", Seasons: []int32{1, 2}, Episodes: map[int32]int32{1: 10, 2: 10}})
	require.NoError(t, err)
	assert.Equal(t, "8776", set.SetID)
	require.Len(t, set.Sets, 2)
	assert.Equal(t, metadata.ImageSetSource{SetID: "41471", Title: set.Sets[1].Title, Creator: "sharivari", Kind: "show", URL: "https://mediux.io/show-set/41471", Images: 11}, set.Sets[1])
	bySet := map[string]int{}
	for _, img := range set.Images {
		bySet[img.SetID]++
	}
	assert.Equal(t, map[string]int{"8776": 13, "41471": 11}, bySet)
}

func TestAMovieIsComposedDirectly(t *testing.T) {
	c, _ := recorded(t)
	src := &mediuxsets.Source{Client: c, Lookup: mediuxsets.Direct{Client: c}}
	set, err := src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindMovie, TMDBID: "328387"})
	require.NoError(t, err)
	assert.Equal(t, "59100", set.SetID, "sexten's, the one with a backdrop too")
	require.Len(t, set.Images, 2)
	assert.Equal(t, "poster", set.Images[0].Type)
	assert.Equal(t, "fanart", set.Images[1].Type)
	assert.Equal(t, "https://mediux.io/movie-set/59100", set.URL)
	_, err = src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindMovie})
	require.ErrorIs(t, err, metadata.ErrNotFound)
	_, err = src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindAlbum, TMDBID: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestEpisodesOfReadsTheRecordsKeys(t *testing.T) {
	assert.Equal(t, map[int32]int32{1: 10, 2: 12}, mediuxsets.EpisodesOf(map[string]int32{"1": 10, "2": 12, "x": 1}))
	assert.Nil(t, mediuxsets.EpisodesOf(nil))
}

// A pinned set (spec.imageSetID) anchors whatever the policy prefers; with
// no id to list the item's other sets it is taken as it is, one request.
func TestAPinnedSetIsTakenAsItIs(t *testing.T) {
	c, calls := recorded(t)
	src := &mediuxsets.Source{Client: c, Lookup: mediuxsets.Direct{Client: c}, Policy: mediux.Policy{PreferredCreators: []string{"RandalPink87"}}}
	set, err := src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries, SetID: "41471"})
	require.NoError(t, err)
	assert.Equal(t, "41471", set.SetID)
	assert.Equal(t, "sharivari", set.Creator)
	assert.Contains(t, set.Reason, "pinned")
	assert.Len(t, set.Images, 24)
	assert.Equal(t, int32(1), calls.Load(), "one request, the set itself")
}

// With the item's ids, a pinned set anchors among the item's sets, which
// fill what it lacks: artificial_grey's set is season 1's titlecards alone.
func TestAPinnedSetsGapsAreFilledFromTheItemsSets(t *testing.T) {
	c, _ := recorded(t)
	src := &mediuxsets.Source{Client: c, Lookup: mediuxsets.Direct{Client: c}, Policy: mediux.Policy{Languages: []string{"English"}}}
	set, err := src.ImageSet(context.Background(), metadata.ImageSetQuery{Kind: commonv1.MediaKindSeries, TMDBID: "105248", SetID: "22523", Seasons: []int32{1, 2}, Episodes: map[int32]int32{1: 10, 2: 10}})
	require.NoError(t, err)
	assert.Equal(t, "22523", set.SetID)
	assert.Equal(t, "artificial_grey", set.Creator)
	assert.Len(t, set.Images, 24)
	assert.Equal(t, "41471", set.Images[0].SetID, "the show poster from the set that has one")
}
