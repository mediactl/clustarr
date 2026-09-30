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
package librarydelete

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...)
	require.NoError(t, RegisterIndexes(context.Background(), builderIndexer{b}))
	return b.Build()
}

func rootFolder(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "movies"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMovie},
	}
}

func heatMovie(folder, mode string, exclude bool) *catalogv1alpha1.Movie {
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "media", Name: "heat",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: mode},
		},
		Spec:   catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
		Status: catalogv1alpha1.MovieStatus{Path: folder},
	}
	if exclude {
		m.Annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	}
	return m
}

func reconcileMovie(t *testing.T, c client.Client) error {
	t.Helper()
	r := &Reconciler{Client: c, kind: kindFor(commonv1.MediaKindMovie)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "heat"}})
	return err
}

func TestDeleteFilesRemovesFolderRecordsAndItem(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	write(t, filepath.Join(folder, "movie.nfo"))
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteFiles, true), &mf)

	require.NoError(t, reconcileMovie(t, c))

	assert.NoDirExists(t, folder)
	assert.DirExists(t, root)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &catalogv1alpha1.Movie{})))
	var ex catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &ex))
	assert.Equal(t, catalogv1alpha1.ExclusionKindMovie, ex.Spec.Kind)
	assert.Equal(t, map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "949"}, ex.Spec.ExternalIDs)
}

func TestDeleteRecordsLeavesTheDiskAlone(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteRecords, false), &mf)

	require.NoError(t, reconcileMovie(t, c))

	assert.FileExists(t, filepath.Join(folder, "Heat.mkv"))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &catalogv1alpha1.Movie{})))
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Empty(t, list.Items)
}

func TestARefusalRemovesNothingAndSaysWhy(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	write(t, filepath.Join(folder, "Ronin.mkv"))
	mine := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	other := mediaFile("ronin-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "ronin"}, filepath.Join(folder, "Ronin.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteFiles, false), &mine, &other)

	require.NoError(t, reconcileMovie(t, c), "a refusal is not retried")

	assert.FileExists(t, filepath.Join(folder, "Heat.mkv"))
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(&mine), &catalogv1alpha1.MediaFile{}))
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &m))
	assert.Contains(t, m.Annotations[catalogv1alpha1.AnnotationDeleteError], "ronin-file")
	assert.Equal(t, catalogv1alpha1.DeleteFiles, m.Annotations[catalogv1alpha1.AnnotationDelete], "the request stays")
}

func TestAnUnknownValueIsRefused(t *testing.T) {
	root := t.TempDir()
	c := newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat"), "everything", false))
	require.NoError(t, reconcileMovie(t, c))
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &m))
	assert.Contains(t, m.Annotations[catalogv1alpha1.AnnotationDeleteError], "everything")
}

// A Series takes its Episodes' MediaFiles with it, found through the
// episode index, and leaves another series' files alone.
func TestDeleteSeriesTakesItsEpisodesFiles(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Andor")
	write(t, filepath.Join(folder, "S01E01.mkv"))
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "media", Name: "andor",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: catalogv1alpha1.DeleteFiles},
		},
		Spec:   catalogv1alpha1.SeriesSpec{TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "movies"},
		Status: catalogv1alpha1.SeriesStatus{Path: folder},
	}
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "andor-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1},
	}
	mf := mediaFile("andor-file", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e01"}, filepath.Join(folder, "S01E01.mkv"))
	keep := mediaFile("other-file", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "other-s01e01"}, filepath.Join(root, "Other", "S01E01.mkv"))
	c := newClient(t, rootFolder(root), s, ep, &mf, &keep)

	r := &Reconciler{Client: c, kind: kindFor(commonv1.MediaKindSeries)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "andor"}})
	require.NoError(t, err)
	assert.NoDirExists(t, folder)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(&keep), &catalogv1alpha1.MediaFile{}))
}

// A retry finds the exclusion it made before and carries on.
func TestTheExclusionIsCreatedOnce(t *testing.T) {
	root := t.TempDir()
	existing := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat"},
		Spec: catalogv1alpha1.ImportExclusionSpec{
			Kind:        catalogv1alpha1.ExclusionKindMovie,
			ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "949"},
		},
	}
	c := newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat"), catalogv1alpha1.DeleteRecords, true), existing)
	require.NoError(t, reconcileMovie(t, c))
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Len(t, list.Items, 1)
}

// Only a new or renewed request passes -- never importarr's own
// delete-error write, which would loop a refusal forever.
func TestDeleteRequestedPredicate(t *testing.T) {
	p := deleteRequested()
	with := func(ann map[string]string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	req := map[string]string{catalogv1alpha1.AnnotationDelete: "files"}
	refused := map[string]string{catalogv1alpha1.AnnotationDelete: "files", catalogv1alpha1.AnnotationDeleteError: "no"}
	assert.True(t, p.Create(event.CreateEvent{Object: with(req)}), "pending at start")
	assert.False(t, p.Create(event.CreateEvent{Object: with(refused)}), "refused before a restart")
	assert.False(t, p.Create(event.CreateEvent{Object: with(nil)}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: with(nil), ObjectNew: with(req)}), "a new request")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: with(req), ObjectNew: with(refused)}), "importarr's own write")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: with(refused), ObjectNew: with(req)}), "Retry clears the error")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: with(req), ObjectNew: with(req)}))
}

var _ = os.Remove // keep os imported for write()

// Another movie's folder inside this one, with no MediaFile recorded for
// it, keeps the folder from being removed; an Author's own Books' folders
// inside its folder do not.
func TestAnotherItemsFolderInsideRefusesButOwnChildrenDoNot(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat 2 (2026)", "unscanned.mkv"))
	other := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat-2"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 2, QualityProfileRef: "hd", RootFolderRef: "movies"},
		Status:     catalogv1alpha1.MovieStatus{Path: filepath.Join(folder, "Heat 2 (2026)")},
	}
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteFiles, false), other)
	require.NoError(t, reconcileMovie(t, c))
	assert.FileExists(t, filepath.Join(folder, "Heat 2 (2026)", "unscanned.mkv"))
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &m))
	assert.Contains(t, m.Annotations[catalogv1alpha1.AnnotationDeleteError], "heat-2")

	authorFolder := filepath.Join(root, "Dostoevsky")
	write(t, filepath.Join(authorFolder, "The Idiot", "idiot.epub"))
	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "media", Name: "dostoevsky",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: catalogv1alpha1.DeleteFiles},
		},
		Spec:   catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL22242A", QualityProfileRef: "ebook", RootFolderRef: "movies"},
		Status: catalogv1alpha1.AuthorStatus{Path: authorFolder},
	}
	ref := "dostoevsky"
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "the-idiot"},
		Spec:       catalogv1alpha1.BookSpec{AuthorRef: &ref, WorkID: "OL1W"},
		Status:     catalogv1alpha1.BookStatus{Path: filepath.Join(authorFolder, "The Idiot")},
	}
	c = newClient(t, rootFolder(root), author, book)
	r := &Reconciler{Client: c, kind: kindFor(commonv1.MediaKindAuthor)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "dostoevsky"}})
	require.NoError(t, err)
	assert.NoDirExists(t, authorFolder)
}

// A scanned movie's status.path is the naming preset's folder, not the one
// on disk (the scan pins no spec.folder for a movie): the delete uses the
// folder its file is in, whole, when status.path is not there (final
// review, finding 2). A file straight in the RootFolder has no folder of
// its own, so only it goes.
func TestAMovieWhoseResolvedFolderIsNotOnDiskUsesItsFilesFolder(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "Heat.1995.1080p.BluRay")
	write(t, filepath.Join(real, "Heat.mkv"))
	write(t, filepath.Join(real, "movie.nfo"))
	mf := mediaFile("heat-file", heat, filepath.Join(real, "Heat.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat (1995)"), catalogv1alpha1.DeleteFiles, false), &mf)
	require.NoError(t, reconcileMovie(t, c))
	assert.NoDirExists(t, real)
	assert.DirExists(t, root)

	flat := filepath.Join(root, "Ronin.mkv")
	write(t, flat)
	write(t, filepath.Join(root, "other.nfo"))
	mf = mediaFile("heat-file", heat, flat)
	c = newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat (1995)"), catalogv1alpha1.DeleteFiles, false), &mf)
	require.NoError(t, reconcileMovie(t, c))
	assert.NoFileExists(t, flat)
	assert.FileExists(t, filepath.Join(root, "other.nfo"))
}
