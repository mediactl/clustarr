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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// Add New redirects to the item's page the moment it creates the object,
// before the library view's next round lists it. The comic, audiobook and
// standalone book pages answer that window as the artist's and author's do:
// the "being added" page, which reloads, never a 404. An author's book is
// the fan-out's, never Add New's, so one whose author the view does not
// hold is still not found, as is a name no object carries.
func TestNonVideoPagesWaitForAnItemAddNewJustCreated(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(
		&catalogv1.Comic{ObjectMeta: metav1.ObjectMeta{Name: "new-comic", Namespace: "default"},
			Spec: catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "4050-1", Kind: catalogv1.ComicKindComic}},
		&catalogv1.Audiobook{ObjectMeta: metav1.ObjectMeta{Name: "new-ab", Namespace: "default"},
			Spec: catalogv1.AudiobookSpec{ASIN: "B000000001"}},
		&catalogv1.Book{ObjectMeta: metav1.ObjectMeta{Name: "new-book", Namespace: "default"},
			Spec: catalogv1.BookSpec{WorkID: "OL1W"}},
		&catalogv1.Book{ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "default"},
			Spec: catalogv1.BookSpec{WorkID: "OL2W", AuthorRef: new("nobody")}},
	).Build()
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Library: func(context.Context) []projection.LibraryItem { return nil },
	})
	for _, path := range []string{"/library/default/comic/new-comic", "/library/default/audiobook/new-ab", "/library/default/book/new-book"} {
		rec := getPath(t, srv, path, false)
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.Contains(t, rec.Body.String(), "is being added", path)
		require.Contains(t, rec.Body.String(), `http-equiv="refresh"`, "%s reloads", path)
	}
	for _, path := range []string{"/library/default/book/orphan", "/library/default/comic/nope", "/library/default/audiobook/nope", "/library/default/book/nope"} {
		require.Equal(t, http.StatusNotFound, getPath(t, srv, path, false).Code, path)
	}
}
