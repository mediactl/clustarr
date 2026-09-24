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

package kitsu_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/kitsu"
)

// Live Kitsu responses captured 2026-09-23 (included items trimmed), plus
// mappings_ambiguous.json: the AoT response with a second item appended,
// since no live TVDB series id maps to two Kitsu anime.
const fixtures = "../../../../test/data/metadata/kitsu/"

// serve routes by path and, for /mappings, the filtered site and id.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		key := r.URL.Path
		if r.URL.Path == "/mappings" {
			key += "|" + r.URL.Query().Get("filter[externalSite]") + "|" + r.URL.Query().Get("filter[externalId]")
		}
		seen = append(seen, key)
		name, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			b, _ := os.ReadFile(fixtures + "notfound.json")
			_, _ = w.Write(b)
			return
		}
		b, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newClient(srv *httptest.Server) *kitsu.Client {
	return kitsu.New(kitsu.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
}

func TestResolveASeriesFromItsTVDBID(t *testing.T) {
	srv, seen := serve(t, map[string]string{
		"/mappings|thetvdb/series|79824": "mappings_thetvdb-series_79824.json",
		"/anime/1555/mappings":           "anime_1555_mappings.json",
	})

	ids, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})

	require.NoError(t, err)
	require.Equal(t, []string{"/mappings|thetvdb/series|79824", "/anime/1555/mappings"}, *seen)
	require.Equal(t, metadata.ExternalIDs{
		extid.KeyKitsu:      "1555",
		extid.KeyMAL:        "1735",
		metadata.KeyAniList: "1735",
		extid.KeyAniDB:      "4880",
		metadata.KeyTVDB:    "79824",
	}, ids, `"thetvdb" 79824/1 is season-scoped and not read; hulu, trakt and aozora have no key`)
}

func TestResolvePrefersTheMostSpecificID(t *testing.T) {
	srv, seen := serve(t, map[string]string{
		"/mappings|myanimelist/anime|1735": "mappings_myanimelist-anime_1735.json",
		"/anime/1555/mappings":             "anime_1555_mappings.json",
	})

	_, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824", extid.KeyMAL: "1735"})

	require.NoError(t, err)
	require.Equal(t, "/mappings|myanimelist/anime|1735", (*seen)[0], "MAL names one anime; a TVDB series can name several")
}

func TestResolveAMangaUsesMangaSites(t *testing.T) {
	srv, _ := serve(t, map[string]string{
		"/mappings|myanimelist/manga|2": "mappings_myanimelist-manga_2.json",
		"/manga/8/mappings":             "manga_8_mappings.json",
	})

	ids, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindComic, metadata.ExternalIDs{extid.KeyMAL: "2", extid.KeyMangaDex: "801513ba-a712-498c-8f57-cae55b38cc92"})

	require.NoError(t, err)
	require.Equal(t, metadata.ExternalIDs{extid.KeyKitsu: "8", extid.KeyMAL: "2", metadata.KeyAniList: "30002"}, ids)
}

func TestResolveByKitsuIDSkipsTheSearch(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/anime/1555/mappings": "anime_1555_mappings.json"})

	_, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyKitsu: "1555", metadata.KeyTVDB: "79824"})

	require.NoError(t, err)
	require.Equal(t, []string{"/anime/1555/mappings"}, *seen)
}

func TestResolveNeverGuesses(t *testing.T) {
	srv, _ := serve(t, map[string]string{
		"/mappings|thetvdb/series|1":      "mappings_empty.json",
		"/mappings|thetvdb/series|267440": "mappings_ambiguous.json",
	})
	c := newClient(srv)

	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound)

	_, err = c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "267440"})
	require.ErrorIs(t, err, kitsu.ErrAmbiguous)
}

func TestResolveRefusesWithoutARequest(t *testing.T) {
	srv, seen := serve(t, nil)
	c := newClient(srv)

	_, err := c.Resolve(context.Background(), commonv1.MediaKindBook, metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyIMDb: "tt0988824"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyAniDB: "4880&include=secret"})
	require.ErrorIs(t, err, kitsu.ErrInvalidID)
	require.Empty(t, *seen)
}

func TestAnUnknownKitsuItemIsErrNotFound(t *testing.T) {
	srv, _ := serve(t, nil)
	_, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyKitsu: "99999999"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestPing(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/mappings|anidb|1": "mappings_empty.json"})
	require.NoError(t, newClient(srv).Ping(context.Background()))
}
