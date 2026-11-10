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

// An artist's and an author's pages list their children inline (2026-11-10
// design §9.2, §9.3): the artist page's albums table grouped by type, the
// author page's books as the season table, each listed through the
// reader's parent index and never another parent's, and a row's toggle
// swaps the row back as its page draws it. The lazily loaded children
// fragment those pages replaced still answers at its route, for a
// bookmarked link, and for no other kind.
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

	// An album's toggle swaps the table row its page draws (AlbumRowView),
	// not the children fragment's item row.
	rec = postForm(t, srv, "/library/default/album/debut/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	row := rec.Body.String()
	require.NotContains(t, row, "<html")
	requireTag(t, row, `data-album="debut"`, `data-slot="table-row"`, `data-monitored="false"`)
	require.NotContains(t, row, `data-child=`, "the fragment's row is not what the page swaps")
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

	// The children fragment still answers its route, for the two parent
	// kinds and no other.
	req := httptest.NewRequest(http.MethodGet, "/library/default/artist/bjork/children", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<html")
	requireTag(t, rec.Body.String(), `data-child="post"`, `data-kind="album"`)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/arrival/children", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "only artists and authors have a children fragment")
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
