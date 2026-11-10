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
package actions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

func TestRequestDeleteWritesTheAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{
			Name: "heat", Namespace: "default",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDeleteError: "refused", "keep": "me"},
		},
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()
	ctx := context.Background()
	get := func() map[string]string {
		var m catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "heat"}, &m))
		return m.Annotations
	}

	_, err := actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", true, true)
	require.NoError(t, err)
	ann := get()
	require.Equal(t, catalogv1alpha1.DeleteFiles, ann[catalogv1alpha1.AnnotationDelete])
	require.Equal(t, "true", ann[catalogv1alpha1.AnnotationDeleteAddExclusion])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteError, "a new request clears the old error")
	require.Equal(t, "me", ann["keep"])

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", false, false)
	require.NoError(t, err)
	ann = get()
	require.Equal(t, catalogv1alpha1.DeleteRecords, ann[catalogv1alpha1.AnnotationDelete])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteAddExclusion)

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindEpisode, "x", true, false)
	require.True(t, errors.Is(err, actions.ErrInvalid), "an episode is deleted with its series: %v", err)
}

// Cancel clears a pending or refused request, so a refused "files" delete
// can be withdrawn, or re-asked as "records", from the ui (final review,
// finding 4).
func TestCancelDeleteClearsTheRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default", Annotations: map[string]string{
			catalogv1alpha1.AnnotationDelete: "files", catalogv1alpha1.AnnotationDeleteAddExclusion: "true",
			catalogv1alpha1.AnnotationDeleteError: "refused", "keep": "me",
		}},
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()
	_, err := actions.CancelDelete(context.Background(), c, "default", commonv1.MediaKindMovie, "heat")
	require.NoError(t, err)
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "heat"}, &m))
	require.Equal(t, map[string]string{"keep": "me"}, m.Annotations)
}

// An album's and a standalone book's Delete (2026-11-10 non-video design
// §9.2, §9.3) patch the three delete annotations and nothing else: the spec
// -- the album's artist and release group, the book's work, profile and
// root -- the status and the other annotations come back as they were,
// and the patch is the kind's existing patch grant, no delete verb.
func TestRequestDeleteOnAnAlbumAndAStandaloneBookPatchesOnlyTheAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "dsotm", Namespace: "default", Annotations: map[string]string{"keep": "me"}},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "pink-floyd", ReleaseGroupID: "rg-dsotm", Monitored: new(true)},
		Status:     catalogv1alpha1.AlbumStatus{Path: "/data/music/Pink Floyd/The Dark Side of the Moon (1973)", HasFile: true},
	}
	profile, root := "ebook", "books"
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "default", Annotations: map[string]string{"keep": "me"}},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W", QualityProfileRef: &profile, RootFolderRef: &root},
		Status:     catalogv1alpha1.BookStatus{Path: "/data/books/A Standalone Book", HasFile: true},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(album, book).Build()
	ctx := context.Background()

	_, err := actions.RequestDelete(ctx, c, "default", commonv1.MediaKindAlbum, "dsotm", true, false)
	require.NoError(t, err)
	var gotAlbum catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "dsotm"}, &gotAlbum))
	require.Equal(t, map[string]string{"keep": "me", catalogv1alpha1.AnnotationDelete: catalogv1alpha1.DeleteFiles}, gotAlbum.Annotations)
	require.Equal(t, album.Spec, gotAlbum.Spec, "the spec is not the delete's to touch")
	require.Equal(t, album.Status, gotAlbum.Status, "nor the status")

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindBook, "standalone", false, false)
	require.NoError(t, err)
	var gotBook catalogv1alpha1.Book
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "standalone"}, &gotBook))
	require.Equal(t, map[string]string{"keep": "me", catalogv1alpha1.AnnotationDelete: catalogv1alpha1.DeleteRecords}, gotBook.Annotations)
	require.Equal(t, book.Spec, gotBook.Spec)
	require.Equal(t, book.Status, gotBook.Status)
}
