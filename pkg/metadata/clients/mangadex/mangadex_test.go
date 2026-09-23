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

package mangadex_test

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
	"github.com/mediactl/clustarr/pkg/metadata/clients/mangadex"
)

// Live MangaDex responses captured 2026-09-23, trimmed.
const (
	fixtures = "../../../../testdata/metadata/mangadex/"
	berserk  = "801513ba-a712-498c-8f57-cae55b38cc92"
)

func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		name, ok := routes[r.URL.Path]
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

func newClient(srv *httptest.Server) *mangadex.Client {
	return mangadex.New(mangadex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, CoverBaseURL: "https://uploads.example"})
}

func TestVolumeMapsTheManga(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/manga/" + berserk: "manga_" + berserk + ".json"})
	c := newClient(srv)

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{extid.KeyMangaDex: berserk})

	require.NoError(t, err)
	require.Equal(t, "cover_art", (*seen)[0].URL.Query().Get("includes[]"))
	require.NotEmpty(t, (*seen)[0].UserAgent(), "MangaDex refuses requests without a User-Agent")
	require.Equal(t, metadata.ExternalIDs{
		extid.KeyMangaDex:     berserk,
		metadata.KeyAniList:   "30002",
		extid.KeyMAL:          "2",
		extid.KeyKitsu:        "8",
		extid.KeyMangaUpdates: "njeqwry",
	}, v.IDs, "links map to ids; the store and raw links do not")
	require.Equal(t, "Berserk", v.Title, "ja-ro when there is no en title")
	require.Equal(t, "manga", v.Kind)
	require.Equal(t, "ongoing", v.Status)
	require.Equal(t, "erotica", v.AgeRating)
	require.Equal(t, "ja", v.OriginalLanguage)
	require.NotNil(t, v.StartYear)
	require.EqualValues(t, 1989, *v.StartYear)
	require.Zero(t, v.IssueCount, "an ongoing series has no lastVolume")
	require.Equal(t, []string{"Action", "Psychological"}, v.Genres)
	require.Equal(t, []string{"Award Winning", "Monsters", "Demons", "Survival"}, v.Tags)
	require.Contains(t, v.AltTitles, metadata.AltTitle{Title: "ベルセルク", Language: "ja"})
	require.NotEmpty(t, v.Description)
	require.Equal(t, []metadata.Image{{Type: metadata.ImageTypePoster, URL: "https://uploads.example/covers/" + berserk + "/81e1c82d-6672-400c-8c58-4ff9bfb89031.jpg"}}, v.Images)
}

func TestIssuesAreCollectedVolumesInNumericOrder(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/manga/" + berserk + "/aggregate": "aggregate_" + berserk + ".json"})
	c := newClient(srv)

	issues, err := c.Issues(context.Background(), berserk)

	require.NoError(t, err)
	numbers := make([]string, 0, len(issues))
	for _, i := range issues {
		numbers = append(numbers, i.Number)
	}
	require.Equal(t, []string{"1", "2", "40", "41"}, numbers, `the uncollected "none" chapters are no issue`)
}

func TestIssuesOfAMangaWithNoVolumesIsEmpty(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/manga/" + berserk + "/aggregate": "aggregate_empty.json"})

	issues, err := newClient(srv).Issues(context.Background(), berserk)

	require.NoError(t, err)
	require.Empty(t, issues, `MangaDex serializes no volumes as [] rather than {}`)
}

func TestAComicVineIDIsNeverReadAsAManga(t *testing.T) {
	srv, seen := serve(t, nil)
	c := newClient(srv)

	_, err := c.Issues(context.Background(), "4050-18257")
	require.ErrorIs(t, err, mangadex.ErrInvalidID)
	require.ErrorIs(t, err, metadata.ErrUnsupported)

	_, err = c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: berserk})
	require.ErrorIs(t, err, metadata.ErrUnsupported, "a uuid under the comicvine key is still the wrong key")

	require.Empty(t, *seen)
}

func TestAnUnknownMangaIsErrNotFound(t *testing.T) {
	srv, _ := serve(t, nil)

	_, err := newClient(srv).Volume(context.Background(), metadata.ExternalIDs{extid.KeyMangaDex: "00000000-0000-4000-8000-000000000000"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestSearchVolumes(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/manga": "search_berserk.json"})

	hits, err := newClient(srv).SearchVolumes(context.Background(), "berserk")

	require.NoError(t, err)
	q := (*seen)[0].URL.Query()
	require.Equal(t, "berserk", q.Get("title"))
	require.Equal(t, []string{"safe", "suggestive", "erotica"}, q["contentRating[]"])
	require.Len(t, hits, 2)
	require.Equal(t, "Berserk", hits[1].Title)
	require.EqualValues(t, 1989, hits[1].Year)
	require.Equal(t, berserk, hits[1].IDs[extid.KeyMangaDex])
	require.Equal(t, "https://uploads.example/covers/"+berserk+"/81e1c82d-6672-400c-8c58-4ff9bfb89031.jpg", hits[1].Poster)
}

func TestResolveReturnsTheLinksCrosswalk(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/manga/" + berserk: "manga_" + berserk + ".json"})
	c := newClient(srv)

	ids, err := c.Resolve(context.Background(), commonv1.MediaKindComic, metadata.ExternalIDs{extid.KeyMangaDex: berserk})
	require.NoError(t, err)
	require.Equal(t, "30002", ids[metadata.KeyAniList])

	_, err = c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

func TestPing(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/manga": "search_berserk.json"})
	require.NoError(t, newClient(srv).Ping(context.Background()))
}
