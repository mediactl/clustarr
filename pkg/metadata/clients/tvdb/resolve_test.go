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

package tvdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// remoteIDServer answers /login and /search/remoteid/<id> from the
// responses recorded from TheTVDB's live API (2026-10-21) -- id -> fixture
// file, or a body to send as it is -- and counts the searches it was asked.
func remoteIDServer(t *testing.T, answers map[string]string) (*tvdb.Client, *atomic.Int32) {
	t.Helper()
	login, err := os.ReadFile("../../../../test/data/metadata/tvdb/login.json")
	require.NoError(t, err)
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_, _ = w.Write(login)
			return
		}
		id, ok := strings.CutPrefix(r.URL.Path, "/search/remoteid/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		searches.Add(1)
		answer, ok := answers[id]
		switch {
		case !ok:
			http.NotFound(w, r)
		case strings.HasPrefix(answer, "status:"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(answer, "{"):
			_, _ = w.Write([]byte(answer))
		default:
			b, err := os.ReadFile("../../../../test/data/metadata/tvdb/" + answer)
			require.NoError(t, err)
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	return tvdb.New("key", "", srv.Client(), srv.URL, metadata.NewLimiter(1000, 1)), &searches
}

// TMDB lists no TVDB id for Cyberpunk: Edgerunners and the anime crosswalks
// do not carry it, so the owner's MAL list could not add it (2026-10-21);
// TheTVDB finds it by its IMDb id.
func TestResolveFindsASeriesTVDBIDByItsIMDbID(t *testing.T) {
	c, searches := remoteIDServer(t, map[string]string{"tt12590266": "search_remoteid_tt12590266.json"})
	ids, err := c.Resolve(context.Background(), commonv1.MediaKindSeries,
		metadata.ExternalIDs{metadata.KeyIMDb: "tt12590266", metadata.KeyTMDB: "105248"})
	require.NoError(t, err)
	assert.Equal(t, metadata.ExternalIDs{metadata.KeyTVDB: "384541"}, ids)
	assert.Equal(t, int32(1), searches.Load())
}

// The Matrix's IMDb id matches TheTVDB's movie, which is no series' id.
func TestResolveTakesNoMovieForASeries(t *testing.T) {
	c, _ := remoteIDServer(t, map[string]string{"tt0133093": "search_remoteid_tt0133093.json"})
	ids, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt0133093"})
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// TheTVDB answers an id it does not know with data: null.
func TestResolveAnIDTheTVDBDoesNotKnowAddsNothing(t *testing.T) {
	c, _ := remoteIDServer(t, map[string]string{"tt9999999999": "search_remoteid_unknown.json"})
	ids, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt9999999999"})
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// The gateway asks every resolver on every refresh: TheTVDB is asked only
// for a series' missing TVDB id, by an IMDb id that is one.
func TestResolveAsksNothingWhenItHasNothingToAdd(t *testing.T) {
	c, searches := remoteIDServer(t, map[string]string{})
	for name, tc := range map[string]struct {
		kind commonv1.MediaKind
		ids  metadata.ExternalIDs
	}{
		"a movie":                {commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt0133093"}},
		"a series with its tvdb": {commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt12590266", metadata.KeyTVDB: "384541"}},
		"no imdb id":             {commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "105248"}},
		"not an imdb id":         {commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt12/../login"}},
		"a bare number":          {commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "12590266"}},
	} {
		ids, err := c.Resolve(context.Background(), tc.kind, tc.ids)
		require.NoError(t, err, name)
		assert.Empty(t, ids, name)
	}
	assert.Zero(t, searches.Load())
}

// Two series under one IMDb id is TheTVDB's own conflict: no guess.
func TestResolveTakesNoSeriesTwoClaim(t *testing.T) {
	c, _ := remoteIDServer(t, map[string]string{
		"tt1587790": `{"status":"success","data":[{"series":{"id":403849}},{"series":{"id":91001}}]}`,
	})
	ids, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt1587790"})
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// A failed search is the gateway's to log; it leaves the ids as they were.
func TestResolveReportsAFailedSearch(t *testing.T) {
	c, _ := remoteIDServer(t, map[string]string{"tt12590266": "status:500"})
	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt12590266"})
	require.Error(t, err)
}

var _ metadata.IDResolver = (*tvdb.Client)(nil)
