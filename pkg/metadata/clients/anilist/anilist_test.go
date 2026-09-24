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

package anilist_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/anilist"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
)

// Live AniList responses captured 2026-09-23 for the very queries the
// client sends.
const fixtures = "../../../../test/data/metadata/anilist/"

var opName = regexp.MustCompile(`^query (\w+)`)

type call struct {
	op   string
	vars map[string]any
}

// serve answers by operation name; a route of "404:<fixture>" answers 404.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		op := opName.FindStringSubmatch(body.Query)[1]
		calls = append(calls, call{op, body.Variables})
		name, ok := routes[op]
		require.True(t, ok, "unexpected operation %s", op)
		status := http.StatusOK
		if len(name) > 4 && name[:4] == "404:" {
			status, name = http.StatusNotFound, name[4:]
		}
		b, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		w.WriteHeader(status)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newClient(srv *httptest.Server) *anilist.Client {
	return anilist.New(anilist.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
}

func TestResolveCrosswalksMALToAniListWithTheKindsMediaType(t *testing.T) {
	srv, calls := serve(t, map[string]string{"IDs": "ids_anime_mal_1735.json"})
	c := newClient(srv)

	ids, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824", extid.KeyMAL: "1735"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"idMal": float64(1735), "type": "ANIME"}, (*calls)[0].vars, "a MAL id is meaningless without its media type")
	require.Equal(t, metadata.ExternalIDs{metadata.KeyAniList: "1735", extid.KeyMAL: "1735"}, ids)
}

func TestResolvePrefersTheAniListID(t *testing.T) {
	srv, calls := serve(t, map[string]string{"IDs": "ids_anime_mal_1735.json"})

	_, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyAniList: "1735", extid.KeyMAL: "9"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": float64(1735), "type": "ANIME"}, (*calls)[0].vars)
}

func TestResolveOfAnUnknownIDIsErrNotFound(t *testing.T) {
	srv, _ := serve(t, map[string]string{"IDs": "404:ids_notfound.json"})

	_, err := newClient(srv).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyAniList: "999999999"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestResolveNeedsAnIDItCanUseAndAKindItKnows(t *testing.T) {
	srv, calls := serve(t, nil)
	c := newClient(srv)

	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})
	require.ErrorIs(t, err, metadata.ErrUnsupported, "AniList has no TVDB ids")
	_, err = c.Resolve(context.Background(), commonv1.MediaKindBook, metadata.ExternalIDs{metadata.KeyAniList: "1"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Resolve(context.Background(), commonv1.MediaKindComic, metadata.ExternalIDs{extid.KeyMAL: "two"})
	require.ErrorIs(t, err, anilist.ErrInvalidID)
	require.Empty(t, *calls)
}

func TestVolumeMapsAManga(t *testing.T) {
	srv, calls := serve(t, map[string]string{"Media": "media_manga_mal_2.json"})

	v, err := newClient(srv).Volume(context.Background(), metadata.ExternalIDs{extid.KeyMAL: "2"})

	require.NoError(t, err)
	require.Equal(t, "MANGA", (*calls)[0].vars["type"])
	require.Equal(t, metadata.ExternalIDs{metadata.KeyAniList: "30002", extid.KeyMAL: "2"}, v.IDs)
	require.Equal(t, "Berserk", v.Title)
	require.Equal(t, "manga", v.Kind)
	require.Equal(t, "ongoing", v.Status, "RELEASING")
	require.Equal(t, "ja", v.OriginalLanguage, "countryOfOrigin JP")
	require.EqualValues(t, 1989, *v.StartYear)
	require.Nil(t, v.EndYear)
	require.Zero(t, v.IssueCount, "volumes is null while releasing")
	require.Contains(t, v.Genres, "Psychological")
	require.Contains(t, v.AltTitles, metadata.AltTitle{Title: "ベルセルク", Language: "ja"})
	require.Contains(t, v.AltTitles, metadata.AltTitle{Title: "Beruseruku"})
	require.NotContains(t, v.AltTitles, metadata.AltTitle{Title: "Berserk", Language: "en"}, "the display title is not also an alternate")
	require.Equal(t, metadata.Rating{Source: "anilist", ValueCentis: 920}, v.Ratings["anilist"], "averageScore 92/100")
	require.Len(t, v.Images, 2)
	require.Equal(t, metadata.ImageTypePoster, v.Images[0].Type)
	require.Equal(t, metadata.ImageTypeBanner, v.Images[1].Type)
	require.NotEmpty(t, v.Description)
}

func TestSearchVolumes(t *testing.T) {
	srv, calls := serve(t, map[string]string{"Search": "search_manga_berserk.json"})

	hits, err := newClient(srv).SearchVolumes(context.Background(), "berserk")

	require.NoError(t, err)
	require.Equal(t, map[string]any{"search": "berserk", "type": "MANGA"}, (*calls)[0].vars)
	require.Len(t, hits, 3)
	require.Equal(t, metadata.SearchHit{
		IDs:    metadata.ExternalIDs{metadata.KeyAniList: "30002", extid.KeyMAL: "2"},
		Title:  "Berserk",
		Year:   1989,
		Poster: "https://s4.anilist.co/file/anilistcdn/media/manga/cover/medium/bx30002-Cul4OeN7bYtn.jpg",
	}, hits[0])
	require.NotContains(t, hits[2].IDs, extid.KeyMAL, "a null idMal is no id")
}

func TestIssuesIsUnsupported(t *testing.T) {
	srv, calls := serve(t, nil)
	_, err := newClient(srv).Issues(context.Background(), "4050-18257")
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	require.Empty(t, *calls)
}

func TestPing(t *testing.T) {
	srv, _ := serve(t, map[string]string{"IDs": "ids_anime_mal_1735.json"})
	require.NoError(t, newClient(srv).Ping(context.Background()))
}
