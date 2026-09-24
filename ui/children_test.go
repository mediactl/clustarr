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

// An artist's and an author's pages reuse the series page's shapes with
// albums and books as the children (spec 2026-09-23-library-page-design):
// one component that loads the children lazily, each row with title, year,
// monitored, file and quality, a toggle through the existing item action
// that swaps the row back, and never another parent's children.
func TestArtistAndAuthorPagesLoadTheirChildrenLazilyAndToggleThem(t *testing.T) {
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
			Spec:       catalogv1.AlbumSpec{ArtistRef: artistRef, ReleaseGroupID: "rg-" + name, Monitored: ptr.To(true)},
			Status: catalogv1.AlbumStatus{
				Metadata: &catalogv1.AlbumMetadata{Title: title, ReleaseDate: year(y)}, Phase: "Imported", TrackFileCount: files,
			},
		}
		if files > 0 {
			a.Status.Quality = &commonv1.Quality{Name: "FLAC"}
		}
		return a
	}
	author := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default"},
		Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: "ebook", RootFolderRef: "books"},
	}
	book := &catalogv1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dispossessed", Namespace: "default"},
		Spec:       catalogv1.BookSpec{AuthorRef: ptr.To("le-guin"), WorkID: "OL2W", Monitored: ptr.To(true)},
		Status: catalogv1.BookStatus{
			Metadata: &catalogv1.BookMetadata{Title: "The Dispossessed", ReleaseDate: year(1974)},
			HasFile:  true, FileFormat: "epub", Phase: "Imported",
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(artist, author, book,
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
	require.Contains(t, page, `data-profile="music-lossless"`)
	require.Contains(t, page, `hx-get="/library/default/artist/bjork/children"`)
	require.Contains(t, page, `hx-trigger="toggle once"`)
	require.NotContains(t, page, `data-child=`, "children load lazily")

	req := httptest.NewRequest(http.MethodGet, "/library/default/artist/bjork/children", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	rows := rec.Body.String()
	require.NotContains(t, rows, "<html")
	requireTag(t, rows, `data-child="debut"`, `data-kind="album"`, `data-monitored="true"`, `data-hasfile="false"`)
	requireTag(t, rows, `data-child="post"`, `data-kind="album"`, `data-monitored="true"`, `data-hasfile="true"`, `data-quality="FLAC"`)
	require.Less(t, indexOf(rows, `data-child="debut"`), indexOf(rows, `data-child="post"`), "oldest first")
	require.NotContains(t, rows, "OK Computer", "another artist's album is never listed")
	require.Contains(t, rows, "1993")
	require.Contains(t, rows, `hx-post="/library/default/album/debut/monitor"`)

	rec = postForm(t, srv, "/library/default/album/debut/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-child="debut"`, `data-kind="album"`, `data-monitored="false"`)
	require.NotContains(t, rec.Body.String(), "<html")

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/author/le-guin/children", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	books := rec.Body.String()
	require.Contains(t, books, "<html", "a plain request gets the page")
	requireTag(t, books, `data-child="dispossessed"`, `data-kind="book"`, `data-monitored="true"`, `data-hasfile="true"`, `data-quality="epub"`)
	require.Contains(t, books, "The Dispossessed")

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/arrival/children", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "only artists and authors have children pages")
}
