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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/trailers"
)

// videosServer serves TMDB's recorded videos responses (2026-11-17: Lamb,
// movie 788929, and Succession, tv 76331) and records what was asked.
func videosServer(t *testing.T) (*tmdb.Client, *[]string) {
	t.Helper()
	files := map[string]string{
		"/movie/788929/videos": "movie-788929-videos.json",
		"/tv/76331/videos":     "tv-76331-videos.json",
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?include_video_language="+r.URL.Query().Get("include_video_language"))
		name, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status_code":34,"status_message":"The resource you requested could not be found."}`))
			return
		}
		body, err := os.ReadFile("../../../../test/data/metadata/tmdb/" + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	return c, &asked
}

// TMDB lists a movie's and a tv show's videos by site and id; the client
// asks for those in its language and in none, and what it answers can be
// picked from: an official trailer on YouTube.
func TestTrailersAreTMDBsVideosForAMovieAndATVShow(t *testing.T) {
	c, asked := videosServer(t)

	movie, err := c.Trailers(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "788929"})
	require.NoError(t, err)
	require.Len(t, movie, 6)
	first := movie[0]
	assert.Equal(t, "YouTube", first.Site)
	assert.Equal(t, "o7inbBuT6z8", first.Key)
	assert.Equal(t, "Trailer", first.Type)
	assert.True(t, first.Official)
	assert.Equal(t, "en", first.Language)
	assert.Equal(t, 1080, first.Height)
	assert.Equal(t, 2022, first.PublishedAt.Year())
	assert.Equal(t, "tmdb", first.Provider)
	picked, ok := trailers.Pick(movie, "en")
	require.True(t, ok)
	assert.Equal(t, "Trailer", picked.Type)
	assert.NotEmpty(t, picked.URL())

	show, err := c.Trailers(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "76331"})
	require.NoError(t, err)
	require.Len(t, show, 5)
	picked, ok = trailers.Pick(show, "en")
	require.True(t, ok)
	assert.Equal(t, "Trailer", picked.Type, "the opening credits and the teasers are passed over")

	assert.Equal(t, []string{"/movie/788929/videos?include_video_language=en,null", "/tv/76331/videos?include_video_language=en,null"}, *asked)
}

// An item TMDB does not know, one without a TMDB id and a kind TMDB lists
// no videos for answer none, without an error.
func TestTrailersOfWhatTMDBCannotLookUpAreNone(t *testing.T) {
	c, asked := videosServer(t)
	for _, tc := range []struct {
		kind commonv1.MediaKind
		ids  metadata.ExternalIDs
	}{
		{commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "999999999"}},
		{commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt9812474"}},
		{commonv1.MediaKindArtist, metadata.ExternalIDs{metadata.KeyTMDB: "788929"}},
	} {
		got, err := c.Trailers(context.Background(), tc.kind, tc.ids)
		require.NoError(t, err)
		assert.Empty(t, got)
	}
	assert.Len(t, *asked, 1, "only the unknown id was asked for")
}
