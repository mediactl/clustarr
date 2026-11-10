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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
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

// Season headers stay items; a season's episodes are Sonarr's table
// (2026-10-07): one row per episode, keyed by the same data attributes,
// whose monitor bookmark swaps the row it sits in.
func TestSeasonHeadersAreItemsAndEpisodesASonarrTable(t *testing.T) {
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
	require.Equal(t, 1, strings.Count(partial, `data-episode-table`), "the episodes are one table")
	require.Equal(t, 2, strings.Count(partial, `data-episode=`), "one row per episode")
	requireTag(t, partial, `data-episode="1"`, `data-slot="table-row"`, `data-monitored="true"`, `data-hasfile="true"`,
		`data-quality="WEBDL-1080p"`, `data-phase="Imported"`, `data-status="file"`)
	require.Contains(t, partial, `hx-target="closest tr"`, "a toggle swaps the row it sits in")
	require.Regexp(t, `data-slot="badge"[^>]*>WEBDL-1080p<`, partial, "the file's quality is a badge")
	require.Regexp(t, `data-episode-title[^>]*>\s*Kassa`, partial, "the title opens the episode's page")
}

// An artist's and an author's pages list their children inline (2026-11-10
// design §9.2, §9.3): the artist page's albums table grouped by type, the
// author page's books as the season table, each listed through the
// reader's parent index and never another parent's, and a row's toggle
// swaps the row back as its page draws it. The lazily loaded children
// fragment those pages replaced (/children, ui/children.go until wave C) is
// gone: nothing reaches it, and its route is not found.
func TestArtistAndAuthorPagesListTheirChildrenInlineAndToggleARow(t *testing.T) {
	year := func(y int) *metav1.Time {
		tm := metav1.NewTime(time.Date(y, time.June, 1, 0, 0, 0, 0, time.UTC))
		return &tm
	}
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "bjork", Namespace: "default"},
		Spec:       catalogv1.ArtistSpec{MusicBrainzID: "mb-bjork", QualityProfileRef: "music-lossless", RootFolderRef: "music"},
	}
	album := func(name, artistRef, title string, y int, files int32) *catalogv1.Album {
		a := &catalogv1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.AlbumSpec{ArtistRef: artistRef, ReleaseGroupID: "rg-" + name, Monitored: new(true)},
			Status: catalogv1.AlbumStatus{
				Metadata: &catalogv1.AlbumMetadata{Title: title, ReleaseDate: year(y), AlbumType: "Album"}, Phase: "Imported", TrackFileCount: files,
			},
		}
		if files > 0 {
			a.Status.FileQuality = &commonv1.Quality{Name: "FLAC"}
		}
		return a
	}
	author := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default"},
		Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: "ebook", RootFolderRef: "books"},
	}
	book := &catalogv1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dispossessed", Namespace: "default"},
		Spec:       catalogv1.BookSpec{AuthorRef: new("le-guin"), WorkID: "OL2W", Monitored: ptr.To(true)},
		Status: catalogv1.BookStatus{
			Metadata: &catalogv1.BookMetadata{Title: "The Dispossessed", ReleaseDate: year(1974)},
			HasFile:  true, FileFormat: "epub", Phase: "Imported",
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).
		WithIndex(&catalogv1.Album{}, ui.AlbumArtistRefField, ui.IndexAlbumByArtistRef).
		WithIndex(&catalogv1.Book{}, ui.BookAuthorRefField, ui.IndexBookByAuthorRef).
		WithObjects(artist, author, book,
			album("post", "bjork", "Post", 1995, 11),
			album("debut", "bjork", "Debut", 1993, 0),
			album("ok-computer", "radiohead", "OK Computer", 1997, 12),
		).Build()
	items := []projection.LibraryItem{
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "bjork"}, Kind: commonv1.MediaKindArtist,
			Tab: projection.TabMusic, Title: "Björk", QualityProfileRef: "music-lossless", Monitored: true,
		},
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "le-guin"}, Kind: commonv1.MediaKindAuthor,
			Tab: projection.TabBooks, Title: "Ursula K. Le Guin", QualityProfileRef: "ebook", Monitored: true,
		},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return items },
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/artist/bjork", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	requireTag(t, page, `data-artist-page="bjork"`, `data-profile="music-lossless"`)
	require.Contains(t, page, `data-album-table`, "the albums are inline, no children fragment to load")
	require.NotContains(t, page, `hx-get="/library/default/artist/bjork/children"`)
	requireTag(t, page, `data-album="debut"`, `data-slot="table-row"`, `data-monitored="true"`, `data-hasfile="false"`)
	requireTag(t, page, `data-album="post"`, `data-slot="table-row"`, `data-monitored="true"`, `data-hasfile="true"`, `data-quality="FLAC"`)
	require.Less(t, indexOf(page, `data-album="post"`), indexOf(page, `data-album="debut"`), "newest first")
	require.NotContains(t, page, "OK Computer", "another artist's album is never listed")
	require.Contains(t, page, `hx-post="/library/default/album/debut/monitor" hx-target="closest tr"`)

	// An album's toggle swaps the table row its page draws (AlbumRowView).
	rec = postForm(t, srv, "/library/default/album/debut/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	row := rec.Body.String()
	require.NotContains(t, row, "<html")
	requireTag(t, row, `data-album="debut"`, `data-slot="table-row"`, `data-monitored="false"`)
	require.NotContains(t, row, `data-child=`, "the retired fragment's item row is not what the page swaps")
	var debut catalogv1.Album
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "debut"}, &debut))
	require.False(t, *debut.Spec.Monitored)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/author/le-guin", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	page = rec.Body.String()
	requireTag(t, page, `data-author-page="le-guin"`)
	require.Contains(t, page, `data-book-table`, "an author's books are inline, as a table")
	requireTag(t, page, `data-book="dispossessed"`, `data-slot="table-row"`, `data-monitored="true"`, `data-hasfile="true"`, `data-quality="epub"`)
	require.NotContains(t, page, `hx-get="/library/default/author/le-guin/children"`)

	// The children fragment is gone with the pages that loaded it: its
	// route answers nothing, to htmx or anyone else, for any kind.
	for _, path := range []string{"/library/default/artist/bjork/children", "/library/default/author/le-guin/children", "/library/default/movie/arrival/children"} {
		for _, htmx := range []bool{true, false} {
			require.Equal(t, http.StatusNotFound, getPath(t, srv, path, htmx).Code, "%s htmx=%t", path, htmx)
		}
	}
}

// An issue's toggle on the comic page swaps the issue row (IssueRowView),
// as an episode's does on the series page.
func TestIssueToggleSwapsItsRow(t *testing.T) {
	srv, c := comicFixture(t)
	rec := postForm(t, srv, "/library/default/issue/saga-001.0/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row := rec.Body.String()
	require.NotContains(t, row, "<html")
	requireTag(t, row, `data-issue="saga-001.0"`, `data-slot="table-row"`, `data-monitored="false"`, `data-number="1"`)
	require.Contains(t, row, `hx-post="/library/default/issue/saga-001.0/monitor" hx-target="closest tr"`)
	var iss catalogv1.Issue
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "saga-001.0"}, &iss))
	require.False(t, *iss.Spec.Monitored)

	// A plain form post redirects as every other action does.
	rec = postForm(t, srv, "/library/default/issue/saga-001.0/monitor", url.Values{"monitored": {"true"}, "return": {"/library/default/comic/saga"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/comic/saga", rec.Header().Get("Location"))
}
