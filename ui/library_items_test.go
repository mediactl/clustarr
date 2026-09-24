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

package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// requireTag finds the one element carrying anchor and asserts every other
// attribute is on that same element, whatever order templ wrote them in.
func requireTag(t *testing.T, body, anchor string, attrs ...string) string {
	t.Helper()
	tag := tagWith(t, body, anchor)
	for _, a := range attrs {
		require.Contains(t, tag, a, "the element carrying %s", anchor)
	}
	return tag
}

// The rest of the library's elements are shadcn-templ items too (design
// 2026-09-23, continuing the cards): the detail header with its actions,
// each season's header, each episode row and each album or book row. Every
// data attribute the earlier tests key on stays on the item element, and a
// row's monitor toggle swaps the item it sits in.

func TestDetailHeaderIsRadarrsHeroWithTheActionsInTheToolbar(t *testing.T) {
	arrival := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Monitored: true, Phase: "Imported", HasFile: true,
		QualityProfileRef: "hd-bluray-web",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{arrival} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/arrival", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The header is Radarr's hero (design 2026-09-24): every data attribute
	// the earlier tests key on stays on it, and it is not a link to itself.
	header := requireTag(t, body, `data-ref="default/arrival"`, `data-hero`, `data-kind="movie"`,
		`data-monitored="true"`, `data-phase="Imported"`, `data-hasfile="true"`, `data-year="2016"`, `data-profile="hd-bluray-web"`)
	require.False(t, strings.HasPrefix(header, "<a "), "the header is not a link to itself")
	require.NotContains(t, body, `data-slot="item-actions"`, "the actions moved to the toolbar")
	at := strings.Index(body, `data-toolbar`)
	require.GreaterOrEqual(t, at, 0, "the page has a toolbar")
	bar := body[at:strings.Index(body, `data-hero`)]
	for _, a := range []string{`data-action="search-now"`, `data-action="refresh-metadata"`} {
		require.Contains(t, bar, a, "the actions live in the toolbar")
	}
	heroAt := strings.Index(body, `data-hero`)
	require.GreaterOrEqual(t, heroAt, 0)
	require.Contains(t, body[heroAt:], `data-action="set-monitored"`, "the bookmark on the title toggles monitoring")
	require.Regexp(t, `data-slot="badge"[^>]*>[^<]*hd-bluray-web`, body, "the quality profile is a badge in the facts")
}

func TestSeasonHeadersAndEpisodeRowsAreItems(t *testing.T) {
	srv, _ := seriesFixture(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/series/andor", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	requireTag(t, page, `data-ref="default/andor"`, `data-hero`)
	requireTag(t, page, `data-season="1"`, `data-slot="item"`, `season-header`, `data-monitored="true"`, `data-episodes="12"`, `data-files="8"`)
	requireTag(t, page, `data-season="2"`, `data-slot="item"`, `data-monitored="false"`)
	require.Contains(t, page, `data-action="set-season-monitored"`)

	req := httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/1", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	partial := rec.Body.String()
	require.Equal(t, 1, strings.Count(partial, `data-slot="item-group"`), "the episodes are one item group")
	require.Equal(t, 2, strings.Count(partial, `data-slot="item"`), "one item per episode")
	requireTag(t, partial, `data-episode="1"`, `data-slot="item"`, `data-monitored="true"`, `data-hasfile="true"`,
		`data-quality="WEBDL-1080p"`, `data-phase="Imported"`)
	require.Contains(t, partial, `hx-target="closest [data-slot=item]"`, "a toggle swaps the item it sits in")
	require.Regexp(t, `data-slot="badge"[^>]*>WEBDL-1080p<`, partial, "the file's quality is a badge")
	require.Regexp(t, `data-slot="item-title"[^>]*>.*?Kassa`, partial, "the number and title are the item's title")
}

func TestChildRowsAreItems(t *testing.T) {
	released := metav1.NewTime(metav1.Now().AddDate(-30, 0, 0))
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "bjork", Namespace: "default"},
		Spec:       catalogv1.ArtistSpec{MusicBrainzID: "mb-bjork", QualityProfileRef: "music-lossless", RootFolderRef: "music"},
	}
	album := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "post", Namespace: "default"},
		Spec:       catalogv1.AlbumSpec{ArtistRef: "bjork", ReleaseGroupID: "rg-post", Monitored: ptr.To(true)},
		Status: catalogv1.AlbumStatus{
			Metadata: &catalogv1.AlbumMetadata{Title: "Post", ReleaseDate: &released}, Phase: "Imported", TrackFileCount: 11,
			Quality: &commonv1.Quality{Name: "FLAC"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(artist, album).Build()
	bjork := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "bjork"}, Kind: commonv1.MediaKindArtist,
		Tab: projection.TabMusic, Title: "Björk", QualityProfileRef: "music-lossless", Monitored: true,
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{bjork} },
	})

	req := httptest.NewRequest(http.MethodGet, "/library/default/artist/bjork/children", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	rows := rec.Body.String()
	require.Equal(t, 1, strings.Count(rows, `data-slot="item-group"`))
	requireTag(t, rows, `data-child="post"`, `data-slot="item"`, `data-kind="album"`, `data-monitored="true"`,
		`data-hasfile="true"`, `data-quality="FLAC"`, `data-phase="Imported"`)
	require.Contains(t, rows, `hx-target="closest [data-slot=item]"`)
	require.Regexp(t, `data-slot="badge"[^>]*>FLAC<`, rows)
	require.Regexp(t, `data-slot="item-title"[^>]*>[^<]*Post`, rows)
}
