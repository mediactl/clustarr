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

package fanart_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/fanart"
)

// The fixtures are written to the v3.2 shape fanart.tv's official client
// documents (fanart-tv/fanart.tv-api src/index.d.ts): the API refuses any
// request without a key, so no live response could be captured.
const fixtures = "../../../../testdata/metadata/fanart/"

type request struct{ path, apiKey, clientKey string }

func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, request{r.URL.Path, r.URL.Query().Get("api_key"), r.URL.Query().Get("client_key")})
		if r.URL.Query().Get("api_key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid API key"}`))
			return
		}
		name, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newClient(t *testing.T, srv *httptest.Server, key string) *fanart.Client {
	t.Helper()
	c, err := fanart.New(fanart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, APIKey: key})
	require.NoError(t, err)
	return c
}

func i32(v int32) *int32 { return &v }

func TestMovieArtworkMapsEveryTypeAndNormalizesItsStrings(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/v3.2/movies/603": "movie_603.json"})
	c := newClient(t, srv, "k")

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "603", metadata.KeyIMDb: "tt0133093"})

	require.NoError(t, err)
	require.Equal(t, []request{{"/v3.2/movies/603", "k", ""}}, *seen, "tmdb is preferred over imdb")
	require.Equal(t, []metadata.Image{
		{Type: metadata.ImageTypePoster, URL: "https://assets.fanart.tv/fanart/the-matrix-4fe6b5d0e0d5a.jpg", Language: "en", Width: 1000, Height: 1426},
		{Type: metadata.ImageTypePoster, URL: "https://assets.fanart.tv/fanart/the-matrix-4fe6b5d0e1a3c.jpg", Language: "de", Width: 1000, Height: 1426},
		{Type: metadata.ImageTypeFanart, URL: "https://assets.fanart.tv/fanart/the-matrix-4f2f7ab0a3b0c.jpg", Width: 1920, Height: 1080},
		{Type: metadata.ImageTypeLogo, URL: "https://assets.fanart.tv/fanart/the-matrix-5226c7e5e1e0f.png", Language: "en", Width: 800, Height: 310},
		{Type: metadata.ImageTypeClearart, URL: "https://assets.fanart.tv/fanart/the-matrix-5123ab0ab1ab2.png", Language: "en", Width: 1000, Height: 562},
		{Type: metadata.ImageTypeDisc, URL: "https://assets.fanart.tv/fanart/the-matrix-50f0de0ecd0e1.png", Language: "en", Width: 1000, Height: 1000},
		{Type: metadata.ImageTypeThumb, URL: "https://assets.fanart.tv/fanart/the-matrix-4f5b6d6b1e5e3.jpg", Language: "en", Width: 1000, Height: 562},
	}, imgs, `"lang":"00" is textless and becomes no language`)
}

func TestMovieArtworkFallsBackToIMDb(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/v3.2/movies/tt0133093": "movie_603.json"})
	c := newClient(t, srv, "k")

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt0133093"})

	require.NoError(t, err)
	require.NotEmpty(t, imgs)
	require.Equal(t, "/v3.2/movies/tt0133093", (*seen)[0].path)
}

func TestSeriesArtworkCarriesSeasonsAndDropsCharacterArt(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/v3.2/tv/79824": "tv_79824.json"})
	c := newClient(t, srv, "k")

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})

	require.NoError(t, err)
	require.Len(t, imgs, 4, "characterart has no ImageType and is left out")
	require.Equal(t, metadata.ImageTypePoster, imgs[0].Type)
	require.Nil(t, imgs[0].Season, "a series poster")
	require.Equal(t, i32(3), imgs[1].Season)
	require.Nil(t, imgs[2].Season, `"season":"all" belongs to the whole series`)
	require.Equal(t, metadata.ImageTypeLogo, imgs[3].Type)
}

func TestArtistArtworkIsTheArtistsOwnNotItsAlbums(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/v3.2/music/a74b1b7f-71a5-4011-9441-d0b5e4122711": "music_a74b1b7f-71a5-4011-9441-d0b5e4122711.json"})
	c := newClient(t, srv, "k")

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindArtist, metadata.ExternalIDs{metadata.KeyMBArtist: "a74b1b7f-71a5-4011-9441-d0b5e4122711"})

	require.NoError(t, err)
	types := make([]metadata.ImageType, 0, len(imgs))
	for _, i := range imgs {
		types = append(types, i.Type)
	}
	require.Equal(t, []metadata.ImageType{metadata.ImageTypeFanart, metadata.ImageTypeLogo, metadata.ImageTypeBanner, metadata.ImageTypeThumb}, types)
}

func TestAlbumArtworkPicksItsReleaseGroupFromEitherAlbumsShape(t *testing.T) {
	const rg = "1b022e01-4da6-387b-8658-8678046e4cef"
	for _, fixture := range []string{"music_albums_" + rg + ".json", "music_albums_v3_map_" + rg + ".json"} {
		t.Run(fixture, func(t *testing.T) {
			srv, _ := serve(t, map[string]string{"/v3.2/music/albums/" + rg: fixture})
			c := newClient(t, srv, "k")

			imgs, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: rg})

			require.NoError(t, err)
			require.NotEmpty(t, imgs)
			require.Equal(t, metadata.ImageTypePoster, imgs[0].Type)
			require.Equal(t, "https://assets.fanart.tv/fanart/nevermind-4fa3e6aa2d9a4.jpg", imgs[0].URL, "not the other album in the list")
		})
	}
}

func TestAlbumMissingFromTheResponseIsErrNotFound(t *testing.T) {
	const rg = "1b022e01-4da6-387b-8658-8678046e4cef"
	srv, _ := serve(t, map[string]string{"/v3.2/music/albums/b1392450-e666-3926-a536-22c65f834433": "music_albums_" + rg + ".json"})
	c := newClient(t, srv, "k")

	_, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "b1392450-e666-3926-a536-22c65f834433"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestARejectedKeyIsErrAuthAndNeverLeaksIntoTheError(t *testing.T) {
	srv, _ := serve(t, nil)
	c := newClient(t, srv, "wrong-SECRET")

	err := c.Ping(context.Background())

	require.ErrorIs(t, err, metadata.ErrAuth)
	require.NotContains(t, err.Error(), "wrong-SECRET")
}

func TestPingTreatsA404AsReachable(t *testing.T) {
	srv, _ := serve(t, nil)
	require.NoError(t, newClient(t, srv, "k").Ping(context.Background()))
}

func TestClientKeyIsSentWhenConfigured(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/v3.2/tv/79824": "tv_79824.json"})
	c, err := fanart.New(fanart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, APIKey: "k", ClientKey: "personal"})
	require.NoError(t, err)

	_, err = c.Artwork(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})

	require.NoError(t, err)
	require.Equal(t, "personal", (*seen)[0].clientKey)
}

func TestNewRequiresAnAPIKey(t *testing.T) {
	_, err := fanart.New(fanart.Config{})
	require.ErrorIs(t, err, fanart.ErrNoAPIKey)
}

func TestArtworkRefusesWhatItCannotServeWithoutARequest(t *testing.T) {
	srv, seen := serve(t, nil)
	c := newClient(t, srv, "k")
	ctx := context.Background()

	_, err := c.Artwork(ctx, commonv1.MediaKindBook, metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Artwork(ctx, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "1429"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Artwork(ctx, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824/../x"})
	require.ErrorIs(t, err, fanart.ErrInvalidID)
	_, err = c.Artwork(ctx, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "0133093"})
	require.ErrorIs(t, err, fanart.ErrInvalidID)

	require.Empty(t, *seen)
}
