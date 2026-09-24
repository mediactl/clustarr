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

// mediaFilesOf lists the namespace's MediaFiles.
func (f *fixture) mediaFilesOf(t *testing.T) []catalogv1alpha1.MediaFile {
	t.Helper()
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(context.Background(), &list, client.InNamespace(f.ns)))
	return list.Items
}

// What the single-issue and book grab targets exposed: an item that holds
// one file got one MediaFile per file of its download. An ebook release
// ships several formats; the one the profile ranks highest is imported and
// the others are rejected, as Radarr's ImportApprovedMovie does ("Movie has
// already been imported").
func TestHandleImportsOnlyTheBestFormatOfABookRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-best-book")
	rf := f.newRoot(t, "books", catalogv1alpha1.RootFolderKindBook)
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "ebook-" + f.ns},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindBook, Cutoff: "AZW3",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "AZW3", Qualities: []string{"AZW3"}},
				{Name: "EPUB", Qualities: []string{"EPUB"}},
				{Name: "MOBI", Qualities: []string{"MOBI"}},
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, qp))
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "the-left-hand-of-darkness", Namespace: f.ns},
		Spec: catalogv1alpha1.BookSpec{
			WorkID: "OL59821W", RootFolderRef: ptr.To(rf.Name), QualityProfileRef: ptr.To(qp.Name),
		},
	}
	require.NoError(t, f.c.Create(ctx, book))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Book(book.Name, f.ns).
		WithStatus(catalogac.BookStatus().WithMetadata(catalogac.BookMetadata().WithTitle("The Left Hand of Darkness"))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var b catalogv1alpha1.Book
		return f.c.Get(ctx, client.ObjectKeyFromObject(book), &b) == nil && b.Status.Metadata != nil &&
			f.c.Get(ctx, client.ObjectKeyFromObject(qp), &catalogv1alpha1.QualityProfile{}) == nil
	})

	contentRoot := dataDir(t, "scratch")
	// Named so the walk meets the worst allowed format first.
	for _, name := range []string{"1 Darkness.mobi", "2 Darkness.epub", "3 Darkness.pdf"} {
		mustWriteSparseFile(t, filepath.Join(contentRoot, name), 1<<20)
	}
	dl := f.createDownloadWith(t, "darkness-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}, qp.Name, nil)
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	assert.Equal(t, "2 Darkness.epub", got.Imported[0].SourcePath, "the best format the profile allows")
	require.Len(t, got.Rejections, 2)
	assert.Contains(t, got.Rejections[0], "1 Darkness.mobi: book the-left-hand-of-darkness already has a file from this download, 2 Darkness.epub")
	assert.Contains(t, got.Rejections[1], "3 Darkness.pdf: quality PDF is not allowed", "a disallowed file keeps its own reason")
	files := f.mediaFilesOf(t)
	require.Len(t, files, 1, "one MediaFile for a single-file book")
	assert.Equal(t, "EPUB", files[0].Spec.Quality.Name)
}

// The same rule for a movie: of two files of one movie in a download, the
// better one (same tier here, so the larger) is its file.
func TestHandleImportsOnlyTheBestFileOfAMovieRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-best-movie")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.WEB-DL-GRP.mkv"), sampleFloor)
	// The walk meets the smaller file first.
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Upgrade", "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), 2*sampleFloor)
	dl := f.createDownload(t, "two-matrix-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	assert.Equal(t, filepath.Join("Upgrade", "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), got.Imported[0].SourcePath)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "movie the-matrix already has a file from this download")
	assert.Len(t, f.mediaFilesOf(t), 1)
}

// And per episode: a pack carrying an episode twice imports the PROPER.
func TestHandleImportsOnlyTheBestFileOfAnEpisode(t *testing.T) {
	ctx := context.Background()
	s := newSeriesFixture(t, "fi-best-episode")
	pack := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(pack, "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(pack, "Breaking.Bad.S01E01.PROPER.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(pack, "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got := s.importTo(t, "best-ep-dl", pack, commonv1.MediaRef{
		Kind: commonv1.MediaKindSeries, Name: s.series,
		Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02"},
	})
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 2)
	var sources []string
	for _, i := range got.Imported {
		sources = append(sources, i.SourcePath)
	}
	assert.ElementsMatch(t, []string{"Breaking.Bad.S01E01.PROPER.1080p.BluRay.x264-GRP.mkv", "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP.mkv"}, sources)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv: episode breaking-bad-s01e01 already has a file")
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, s.api.List(ctx, &list, client.InNamespace(s.ns)))
	assert.Len(t, list.Items, 2)
}
