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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The crash window these tests reproduce: a delivery applies its
// MediaFiles, then dies -- the pod is killed, or patchImport's status write
// fails -- before status.import and the dedup record are written. The
// redelivery finds this import's own MediaFile among the item's existing
// files. Compared against itself, the file is never an upgrade, so every
// file was rejected, status.import read "every file rejected", and the
// Download controller blocklisted the release, searched again and had the
// engine delete the download's data. The redelivery must instead finish the
// import it already made.

// deliver runs one delivery of dl's import task, carrying the Download's
// real UID as a producer's task does, and returns the task so a test can
// deliver it again.
func (f *fixture) deliver(t *testing.T, dl *downloadv1alpha1.Download) *fakeMessage {
	t.Helper()
	ctx := context.Background()
	var created downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(dl), &created))
	require.NotEmpty(t, created.UID)
	msg := newImportTaskMessage(t, f.ns, dl.Name, string(created.UID))
	require.NoError(t, f.worker.Handle(ctx, msg))
	return msg
}

// crashBeforeTheStatusWrite leaves dl as a delivery that died after its
// MediaFiles were applied leaves it: the MediaFiles and the library files
// stay, status.import was never written (it is released here under its one
// owner, k8s.ManagerImportarr) and there is no dedup record. It waits until
// the worker's cached read of the Download sees no status.import, so the
// redelivery cannot take the "already imported" fast path.
func (f *fixture) crashBeforeTheStatusWrite(t *testing.T, dl *downloadv1alpha1.Download, msg *fakeMessage) {
	t.Helper()
	ctx := context.Background()
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerImport,
		downloadac.Download(dl.Name, f.ns).WithStatus(downloadac.DownloadStatus()))
	require.NoError(t, err)
	uid := msg.uid(t)
	if err := f.bus.KV(events.BucketDedup).Delete(ctx, fileimport.DedupKey(uid)); err != nil &&
		!errors.Is(err, events.ErrKeyNotFound) {
		require.NoError(t, err)
	}
	waitFor(t, 5*time.Second, func() bool {
		var cached downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKeyFromObject(dl), &cached) == nil && cached.Status.Import == nil
	})
	msg.attempt = 2
}

// uid is the Download UID m's task carries.
func (m *fakeMessage) uid(t *testing.T) string {
	t.Helper()
	var task schema.ImportTask
	require.NoError(t, schema.Decode(m.env.Schema, m.env.Data, &task))
	require.NotEmpty(t, task.DownloadRef.UID)
	return task.DownloadRef.UID
}

// waitForCachedMediaFiles waits until the cache the non-video and episode
// paths read existing files through lists n MediaFiles in the namespace, so
// a redelivery is decided against them and not against a cache that has
// not caught up (which would let the old self-comparison pass unseen).
func (f *fixture) waitForCachedMediaFiles(t *testing.T, n int) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return f.c.List(context.Background(), &list, client.InNamespace(f.ns)) == nil && len(list.Items) == n
	})
}

// requireSameImport asserts that the redelivery reported the import the
// first delivery made -- the same MediaFiles at the same paths, every one
// still on disk -- with nothing rejected.
func requireSameImport(t *testing.T, first, again *downloadv1alpha1.ImportState) {
	t.Helper()
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, again.State, "message %q, rejections %v", again.Message, again.Rejections)
	assert.Empty(t, again.Rejections)
	require.Len(t, again.Imported, len(first.Imported))
	for i := range first.Imported {
		assert.Equal(t, first.Imported[i].MediaFileRef, again.Imported[i].MediaFileRef)
		assert.Equal(t, first.Imported[i].DestPath, again.Imported[i].DestPath)
		_, err := os.Stat(again.Imported[i].DestPath)
		require.NoError(t, err, "the imported file stays in the library")
	}
}

// A movie's redelivery after the crash window finishes the import: its own
// MediaFile is neither compared against nor recycled. The second row is
// that file once catalogarr has probed it as an ffmpeg HEVC encode -- a
// release Tdarr or a re-encoder made -- which the transcoded gate reads as
// final; it is still this import's own file, not one it would replace.
func TestARedeliveryAfterTheMovieFileLandedFinishesTheImport(t *testing.T) {
	cases := []struct {
		name  string
		probe *commonv1.MediaInfo
	}{
		{name: "the file as imported"},
		{name: "the file probed as an ffmpeg HEVC encode", probe: &commonv1.MediaInfo{
			Container: "matroska", VideoCodec: "hevc", VideoEncoder: "Lavc60.31.102 libx265",
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, fmt.Sprintf("fi-redeliver-movie-%d", i))
			bin := f.withRecycleBin(t)
			contentRoot := dataDir(t, "scratch")
			mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
			dl := f.createDownload(t, "matrix-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})

			msg := f.deliver(t, dl)
			first := f.importState(t, dl).Status.Import
			require.Equal(t, downloadv1alpha1.ImportPhaseImported, first.State, "message %q, rejections %v", first.Message, first.Rejections)
			require.Len(t, first.Imported, 1)
			if c.probe != nil {
				_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalog,
					catalogac.MediaFile(first.Imported[0].MediaFileRef, f.ns).WithStatus(
						catalogac.MediaFileStatus().WithProbeHash("probe-1").WithMediaInfo(*c.probe)))
				require.NoError(t, err)
				var mf catalogv1alpha1.MediaFile
				require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: first.Imported[0].MediaFileRef}, &mf))
				require.True(t, mf.Transcoded(), "the row's premise: the probe reads as transcoded")
			}

			f.crashBeforeTheStatusWrite(t, dl, msg)
			require.NoError(t, f.worker.Handle(ctx, msg))

			requireSameImport(t, first, f.importState(t, dl).Status.Import)
			files := f.mediaFilesOf(t)
			require.Len(t, files, 1)
			assert.Equal(t, first.Imported[0].MediaFileRef, files[0].Name)
			binned, err := os.ReadDir(bin)
			require.NoError(t, err)
			assert.Empty(t, binned, "the import's own file is not recycled")
		})
	}
}

// Excluding the import's own file must not weaken the upgrade rule: a file
// another Download imported is compared as before, and so is one imported
// by an earlier Download of the same name -- a grab names its Download
// after the target and release, so re-grabbing a release reuses the name --
// which the new Download did not import, since it imported nothing before
// it was created.
func TestARedeliveryStillComparesEveryFileItDidNotImport(t *testing.T) {
	cases := []struct {
		name string
		// sameName: the existing file records the new Download's name, but
		// was imported an hour before that Download was created.
		sameName bool
	}{
		{name: "a file another Download imported"},
		{name: "a file an earlier Download of the same name imported", sameName: true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, fmt.Sprintf("fi-redeliver-compare-%d", i))
			movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}
			release := "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"

			earlier := dataDir(t, "scratch")
			mustWriteSparseFile(t, filepath.Join(earlier, release), sampleFloor)
			earlierDL := f.createDownload(t, "earlier-dl", earlier, movie)
			f.deliver(t, earlierDL)
			got := f.importState(t, earlierDL).Status.Import
			require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
			existing := got.Imported[0].MediaFileRef
			if c.sameName {
				var mf catalogv1alpha1.MediaFile
				require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: existing}, &mf))
				_, err := k8s.Apply(ctx, f.c, fileimport.FieldManager, catalogac.MediaFile(existing, f.ns).WithSpec(
					catalogac.MediaFileSpec().
						WithMediaRef(mf.Spec.MediaRef).WithPath(mf.Spec.Path).WithSizeBytes(mf.Spec.SizeBytes).
						WithModTime(mf.Spec.ModTime).WithQuality(mf.Spec.Quality).WithRevision(mf.Spec.Revision).
						WithReleaseType(mf.Spec.ReleaseType).WithReleaseGroup(mf.Spec.ReleaseGroup).
						WithFormatScore(mf.Spec.FormatScore).WithProfileHash(mf.Spec.ProfileHash).WithOriginal(true).
						WithImportedFrom(catalogac.ImportSource().
							WithDownloadRef("again-dl").
							WithReleaseTitle(mf.Spec.ImportedFrom.ReleaseTitle).
							WithImportedAt(metav1.NewTime(time.Now().Add(-time.Hour))))))
				require.NoError(t, err)
			}

			again := dataDir(t, "scratch")
			mustWriteSparseFile(t, filepath.Join(again, release), sampleFloor)
			dl := f.createDownload(t, "again-dl", again, movie)
			f.deliver(t, dl)

			got = f.importState(t, dl).Status.Import
			require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State, "message %q, rejections %v", got.Message, got.Rejections)
			require.Len(t, got.Rejections, 1)
			assert.Contains(t, got.Rejections[0], "the candidate's custom-format score is not higher than the existing file's",
				"the existing file is compared: the same release is no upgrade over it")
			err := f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: existing}, &catalogv1alpha1.MediaFile{})
			require.NoError(t, err, "the existing file's MediaFile stays")
			assert.Len(t, f.mediaFilesOf(t), 1)
		})
	}
}

// An episode's redelivery after the crash window finishes the import.
func TestARedeliveryAfterTheEpisodeFileLandedFinishesTheImport(t *testing.T) {
	ctx := context.Background()
	s := newSeriesFixture(t, "fi-redeliver-episode")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Breaking.Bad.S01E03.1080p.WEB-DL.DD5.1.H.264-GRP.mkv"), sampleFloor)
	dl := s.createDownloadWith(t, "ep-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e03"}, "", nil)

	msg := s.deliver(t, dl)
	first := s.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, first.State, "message %q, rejections %v", first.Message, first.Rejections)
	require.Len(t, first.Imported, 1)
	s.waitForCachedMediaFiles(t, 1)

	s.crashBeforeTheStatusWrite(t, dl, msg)
	require.NoError(t, s.worker.Handle(ctx, msg))

	requireSameImport(t, first, s.importState(t, dl).Status.Import)
	assert.Len(t, s.mediaFilesOf(t), 1)
}

// An album's redelivery after the crash window finishes the import: its
// own tracks are not "files the album already has", which only a manual
// import may add to.
func TestARedeliveryAfterTheAlbumTracksLandedFinishesTheImport(t *testing.T) {
	ctx := context.Background()
	a := newAlbumFixture(t, "fi-redeliver-album", []string{"r1", "r2"}, "FLAC")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "01 - Airbag.flac"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "02 - Paranoid Android.flac"), 1<<20)
	dl := a.createDownloadWith(t, "album-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: a.album.Name}, "", nil)

	msg := a.deliver(t, dl)
	first := a.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, first.State, "message %q, rejections %v", first.Message, first.Rejections)
	require.Len(t, first.Imported, 2)
	a.waitForCachedMediaFiles(t, 2)

	a.crashBeforeTheStatusWrite(t, dl, msg)
	require.NoError(t, a.worker.Handle(ctx, msg))

	requireSameImport(t, first, a.importState(t, dl).Status.Import)
	assert.Len(t, a.mediaFiles(t), 2)
	assert.Empty(t, a.binFiles(t))
}

// A book's redelivery after the crash window finishes the import: its own
// file is not compared against itself (a book holds one file, so an
// automatic import must be an upgrade over the one it has).
func TestARedeliveryAfterTheBookFileLandedFinishesTheImport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-redeliver-book")
	_, book := f.newBook(t)
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The Dispossessed.epub"), 1<<20)
	dl := f.createDownloadWith(t, "book-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}, "", nil)

	msg := f.deliver(t, dl)
	first := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, first.State, "message %q, rejections %v", first.Message, first.Rejections)
	require.Len(t, first.Imported, 1)
	f.waitForCachedMediaFiles(t, 1)

	f.crashBeforeTheStatusWrite(t, dl, msg)
	require.NoError(t, f.worker.Handle(ctx, msg))

	requireSameImport(t, first, f.importState(t, dl).Status.Import)
	assert.Len(t, f.mediaFilesOf(t), 1)
}

// A book whose file is already gone from disk still has its MediaFile
// removed when a new file replaces it, as the movie and episode paths
// already did: a missing file is nothing to recycle, not a reason to keep
// a MediaFile naming a path that holds nothing.
func TestAReplacedBookWhoseFileIsGoneLosesItsMediaFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-replace-gone-book")
	_, book := f.newBook(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}

	first := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(first, "The Dispossessed.epub"), 1<<20)
	dl := f.createDownloadWith(t, "first-dl", first, target, "", nil)
	f.deliver(t, dl)
	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	old := got.Imported[0]
	require.NoError(t, os.Remove(old.DestPath))
	f.waitForCachedMediaFiles(t, 1)

	second := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(second, "The Dispossessed.azw3"), 1<<20)
	dl = f.createDownloadWith(t, "second-dl", second, target, "",
		map[string]string{importtarget.AnnotationImportOverride: "true"})
	f.deliver(t, dl)
	got = f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	require.NotEqual(t, old.DestPath, got.Imported[0].DestPath)

	err := f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: old.MediaFileRef}, &catalogv1alpha1.MediaFile{})
	assert.True(t, apierrors.IsNotFound(err), "the replaced file's MediaFile is deleted: %v", err)
	files := f.mediaFilesOf(t)
	require.Len(t, files, 1)
	assert.Equal(t, got.Imported[0].MediaFileRef, files[0].Name)
}

// A placement outside the root folder is refused before anything is
// written, and reported at once as Blocked naming the path -- not retried,
// and not "every file rejected", which grabarr reads as a bad release to
// blocklist and delete. An album's resolved status.path is used as it is,
// so one left pointing elsewhere (its root folder changed since) is
// such a placement.
func TestAPlacementOutsideTheRootFolderBlocksTheImport(t *testing.T) {
	ctx := context.Background()
	a := newAlbumFixture(t, "fi-outside-root", []string{"r1", "r2"}, "FLAC")
	elsewhere := filepath.Join(dataDir(t, "media"), "Radiohead", "OK Computer")
	_, err := k8s.PatchStatus(ctx, a.c, k8s.ManagerCatalogFanout,
		catalogac.Album(a.album.Name, a.ns).WithStatus(catalogac.AlbumStatus().WithPath(elsewhere)))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.Album
		return a.c.Get(ctx, client.ObjectKeyFromObject(a.album), &got) == nil && got.Status.Path == elsewhere
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "01 - Airbag.flac"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "02 - Paranoid Android.flac"), 1<<20)
	dl := a.createDownloadWith(t, "album-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: a.album.Name}, "", nil)
	msg := a.deliver(t, dl)
	require.EqualValues(t, 1, msg.Attempt(), "the first of the consumer's deliveries, so only a block reports it")

	got := a.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	assert.NotEqual(t, downloadv1alpha1.ImportMessageEveryFileRejected, got.Message)
	assert.Contains(t, got.Message, "is not inside root folder path")
	assert.Contains(t, got.Message, a.root.Spec.Path)
	assert.Empty(t, got.Imported)
	assert.Empty(t, got.Rejections)
	_, err = os.Stat(elsewhere)
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing was placed outside the root folder")
	assert.Empty(t, a.mediaFiles(t))
}

// A movie whose folder override climbs out of the library renders no
// destination (pkg/naming/catalogctx refuses it). That is the movie's
// fault, not the release's: the import reads Blocked naming the cause, as
// a non-video render failure does, never "every file rejected", which
// grabarr reads as a bad release to blocklist and delete.
func TestAnUnrenderableMoviePathBlocksTheImport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-unrenderable-movie")
	var movie catalogv1alpha1.Movie
	require.NoError(t, f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &movie))
	patched := movie.DeepCopy()
	patched.Spec.Folder = new("../../etc")
	require.NoError(t, f.c.Patch(ctx, patched, client.MergeFrom(&movie)))
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.Movie
		return f.c.Get(ctx, client.ObjectKeyFromObject(patched), &got) == nil &&
			got.Spec.Folder != nil && *got.Spec.Folder == "../../etc"
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
	dl := f.createDownload(t, "matrix-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	msg := f.deliver(t, dl)
	require.EqualValues(t, 1, msg.Attempt(), "the first of the consumer's deliveries, so only a block reports it")

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	assert.NotEqual(t, downloadv1alpha1.ImportMessageEveryFileRejected, got.Message)
	assert.Contains(t, got.Message, "could not render a destination path")
	assert.Empty(t, got.Imported)
	assert.Empty(t, got.Rejections)
	assert.Empty(t, f.mediaFilesOf(t))
}
