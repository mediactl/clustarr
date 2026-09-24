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

package fileimport_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The grab path (X4a) creates a Download whose spec.target is the Book
// itself -- {kind: book, name}, no keys -- with the book's own
// qualityProfileRef, which is empty when the book inherits its author's.
// Its completed download imports under the author's folder by pkg/naming's
// book preset, against the author's profile and root folder, and the
// MediaFile backs the Book.
func TestHandleImportsACompletedDownloadOfABook(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-book")
	rf := f.newRoot(t, "books", catalogv1alpha1.RootFolderKindBook)
	profile := f.nonVideoProfile(t, catalogv1alpha1.ProfileMediaKindBook, "EPUB", "AZW3")

	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "ursula-k-le-guin", Namespace: f.ns},
		Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL31353A", QualityProfileRef: profile, RootFolderRef: rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, author))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Author(author.Name, f.ns).
		WithStatus(catalogac.AuthorStatus().WithMetadata(catalogac.AuthorMetadata().WithName("Ursula K. Le Guin"))))
	require.NoError(t, err)
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "the-dispossessed", Namespace: f.ns},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL59863W", AuthorRef: ptr.To(author.Name)},
	}
	require.NoError(t, f.c.Create(ctx, book))
	released := metav1.NewTime(time.Date(1974, 5, 1, 0, 0, 0, 0, time.UTC))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Book(book.Name, f.ns).
		WithStatus(catalogac.BookStatus().WithMetadata(catalogac.BookMetadata().WithTitle("The Dispossessed").WithReleaseDate(released))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var a catalogv1alpha1.Author
		var b catalogv1alpha1.Book
		return f.c.Get(ctx, client.ObjectKeyFromObject(author), &a) == nil && a.Status.Metadata != nil &&
			f.c.Get(ctx, client.ObjectKeyFromObject(book), &b) == nil && b.Status.Metadata != nil
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Ursula K. Le Guin - The Dispossessed (1974) [EPUB]",
		"The Dispossessed.epub"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Ursula K. Le Guin - The Dispossessed (1974) [EPUB]", "cover.jpg"), 4096)
	dl := f.createDownloadWith(t, "dispossessed-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}, "", nil)
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	dest := got.Imported[0].DestPath
	assert.Equal(t, filepath.Join(rf.Spec.Path, "Ursula K. Le Guin", "The Dispossessed", "Ursula K. Le Guin.epub"), dest)
	_, err = os.Stat(dest)
	require.NoError(t, err)

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Imported[0].MediaFileRef}, &mf))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}, mf.Spec.MediaRef)
	assert.Equal(t, "EPUB", mf.Spec.Quality.Name)
	assert.Equal(t, commonv1.ReleaseTypeBook, mf.Spec.ReleaseType)
}

// The grab path's single-issue Download targets the Issue itself --
// {kind: issue, name}, no keys, the comic's qualityProfileRef -- where the
// only issue coverage so far went through the comic annotation. It imports
// into the comic's folder by pkg/naming's issue preset and backs the Issue.
func TestHandleImportsACompletedDownloadOfAnIssue(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-issue")
	rf := f.newRoot(t, "comics", catalogv1alpha1.RootFolderKindComic)
	profile := f.nonVideoProfile(t, catalogv1alpha1.ProfileMediaKindComic, "CBZ")

	comic := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: f.ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-49901",
			QualityProfileRef: profile, RootFolderRef: rf.Name,
		},
	}
	require.NoError(t, f.c.Create(ctx, comic))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Comic(comic.Name, f.ns).
		WithStatus(catalogac.ComicStatus().WithMetadata(catalogac.ComicMetadata().WithTitle("Saga").WithYear(2012))))
	require.NoError(t, err)
	issue := &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-00012.0", Namespace: f.ns},
		Spec:       catalogv1alpha1.IssueSpec{ComicRef: comic.Name, Number: "12", CalculatedNumberCentis: 1200},
	}
	require.NoError(t, f.c.Create(ctx, issue))
	waitFor(t, 5*time.Second, func() bool {
		var c catalogv1alpha1.Comic
		return f.c.Get(ctx, client.ObjectKeyFromObject(comic), &c) == nil && c.Status.Metadata != nil &&
			f.c.Get(ctx, client.ObjectKeyFromObject(issue), &catalogv1alpha1.Issue{}) == nil
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Saga 012 (2013) (Digital).cbz"), 1<<20)
	dl := f.createDownloadWith(t, "saga-12-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: issue.Name}, profile, nil)
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	assert.Equal(t, filepath.Join(rf.Spec.Path, "Saga", "Saga c12.cbz"), got.Imported[0].DestPath, "pkg/naming's comic preset carries no year")

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Imported[0].MediaFileRef}, &mf))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: issue.Name}, mf.Spec.MediaRef)
	assert.Equal(t, "CBZ", mf.Spec.Quality.Name)
}
