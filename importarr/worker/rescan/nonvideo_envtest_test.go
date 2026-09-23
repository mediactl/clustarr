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

package rescan_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// smallFile is well under fsops' 50 MiB video "sample" floor: a non-video
// file this size must still be attributed, because a track, an ebook or a
// comic issue is routinely smaller than any video sample.
const smallFile = 1 << 20

func date(year int) *metav1.Time {
	t := metav1.NewTime(time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC))
	return &t
}

// waitCached waits until the manager's cache serves obj, so the worker's
// List sees it.
func waitCached(t *testing.T, ctx context.Context, c client.Client, obj client.Object, ready func() bool) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && ready()
	})
}

// mediaFilesIn returns the namespace's MediaFiles sorted by path.
func mediaFilesIn(t *testing.T, ctx context.Context, c client.Client, ns string, want int) []catalogv1alpha1.MediaFile {
	t.Helper()
	var list catalogv1alpha1.MediaFileList
	waitFor(t, 10*time.Second, func() bool {
		return c.List(ctx, &list, client.InNamespace(ns)) == nil && len(list.Items) == want
	})
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Spec.Path < list.Items[j].Spec.Path })
	return list.Items
}

// unmatchedByPath indexes a checkpoint's unmatched entries.
func unmatchedByPath(p rescan.Progress) map[string]rescan.UnmatchedFile {
	out := map[string]rescan.UnmatchedFile{}
	for _, u := range p.Unmatched {
		out[u.Path] = u
	}
	return out
}

// assertSpecOnly asserts the MediaFile split on managedFields: every listed
// spec leaf is importarr-worker's, and nothing in this package ever wrote any
// part of status.
func assertSpecOnly(t *testing.T, mf catalogv1alpha1.MediaFile, leaves ...string) {
	t.Helper()
	for _, leaf := range leaves {
		assert.Equal(t, string(rescan.FieldManager), managerFor(t, mf.ManagedFields, "", leaf), "%s owner", leaf)
	}
	for _, e := range mf.ManagedFields {
		if e.Manager == string(rescan.FieldManager) {
			assert.Empty(t, e.Subresource, "importarr must never write any MediaFile status field")
		}
	}
}

// createArtistAlbum creates an Artist and one Album under root with cached
// metadata, and gives the Album a status field of its own under catalogarr,
// so the test can prove the scanner leaves the catalog item untouched.
func createArtistAlbum(t *testing.T, ctx context.Context, f *fixture) *catalogv1alpha1.Album {
	t.Helper()
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: f.ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "music", RootFolderRef: f.rf.Name,
		},
	}
	require.NoError(t, f.c.Create(ctx, artist))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Artist(artist.Name, f.ns).
		WithStatus(catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Radiohead"))))
	require.NoError(t, err)

	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: f.ns},
		Spec: catalogv1alpha1.AlbumSpec{
			ArtistRef: artist.Name, ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433",
		},
	}
	require.NoError(t, f.c.Create(ctx, album))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Album(album.Name, f.ns).
		WithStatus(catalogac.AlbumStatus().WithMetadata(
			catalogac.AlbumMetadata().WithTitle("OK Computer").WithReleaseDate(*date(1997)))))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.Album(album.Name, f.ns).
		WithStatus(catalogac.AlbumStatus().WithTrackFileCount(3)))
	require.NoError(t, err)

	waitCached(t, ctx, f.c, artist, func() bool { return artist.Status.Metadata != nil })
	waitCached(t, ctx, f.c, album, func() bool { return album.Status.Metadata != nil && album.Status.TrackFileCount == 3 })
	return album
}

// A music root attributes a track to the existing Album the layout names,
// writes MediaFileSpec only, and never touches the Album. Everything it
// cannot attribute -- an artist and album nobody added, a track outside the
// <artist>/<album>/ layout -- is unmatched with a reason, and no Artist or
// Album is guessed into existence.
func TestHandleAttributesAMusicFileToAnExistingAlbumOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-music", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	album := createArtistAlbum(t, ctx, f)

	track := filepath.Join(f.root, "Radiohead", "OK Computer (1997)", "01 - Airbag.flac")
	mustWriteFile(t, track, smallFile)
	lossy := filepath.Join(f.root, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.mp3")
	mustWriteFile(t, lossy, smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Radiohead", "OK Computer (1997)", "cover.jpg"), 4096)
	mustWriteFile(t, filepath.Join(f.root, "Unknown Band", "Some Album (2001)", "01.flac"), smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Radiohead", "01 - Loose Track.flac"), smallFile)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(4), got.FilesSeen, "the small audio files are media, not samples")
	assert.Equal(t, int64(1), got.FilesSkipped, "cover.jpg")
	assert.Equal(t, int64(2), got.FilesMatched)
	assert.Zero(t, got.ItemsCreated, "a non-video rescan never creates a catalog item")
	unmatched := unmatchedByPath(got)
	require.Len(t, unmatched, 2)
	assert.Contains(t, unmatched[filepath.Join("Unknown Band", "Some Album (2001)", "01.flac")].Reason, "no existing album")
	assert.Contains(t, unmatched[filepath.Join("Radiohead", "01 - Loose Track.flac")].Reason, "<artist>/<album>/<track>")

	files := mediaFilesIn(t, ctx, f.c, f.ns, 2)
	mf, mp3 := files[0], files[1]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: album.Name}, mf.Spec.MediaRef)
	assert.Equal(t, track, mf.Spec.Path)
	assert.Equal(t, int64(smallFile), mf.Spec.SizeBytes)
	assert.Equal(t, commonv1.ReleaseTypeAlbum, mf.Spec.ReleaseType)
	assert.Equal(t, "FLAC", mf.Spec.Quality.Name, "Lidarr's rule: .flac is FLAC unless a 24-bit marker is declared")
	assertSpecOnly(t, mf, "spec.mediaRef", "spec.path", "spec.sizeBytes", "spec.modTime", "spec.releaseType", "spec.quality.name")

	assert.Equal(t, lossy, mp3.Spec.Path)
	assert.Empty(t, mp3.Spec.Quality.Name, "an .mp3's tier is a bitrate band that needs a probe, so none is frozen")
	assert.Empty(t, managerFor(t, mp3.ManagedFields, "", "spec.quality"), "nothing claimed for an unknown quality")

	var artists catalogv1alpha1.ArtistList
	require.NoError(t, f.c.List(ctx, &artists, client.InNamespace(f.ns)))
	assert.Len(t, artists.Items, 1, "no Artist guessed from \"Unknown Band\"")
	var albums catalogv1alpha1.AlbumList
	require.NoError(t, f.c.List(ctx, &albums, client.InNamespace(f.ns)))
	assert.Len(t, albums.Items, 1)

	var after catalogv1alpha1.Album
	require.NoError(t, f.c.Get(ctx, client.ObjectKeyFromObject(album), &after))
	assert.Equal(t, int32(3), after.Status.TrackFileCount, "the album's own status is not the scanner's")
	require.NotNil(t, after.Status.Metadata)
	for _, e := range after.ManagedFields {
		assert.NotEqual(t, string(rescan.FieldManager), e.Manager, "the scanner wrote to the Album")
	}
}

// A comic root attributes an issue file to the Issue of the Comic the series
// folder names, freezing CBZ from the extension. An issue number the comic
// has no Issue for is unmatched: Issues come from the Comic controller's
// metadata fan-out, never from the scanner.
func TestHandleAttributesAComicFileToAnExistingIssueOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-comic", catalogv1alpha1.RootFolderKindComic, "", catalogv1alpha1.ScanModeFull)

	comic := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: f.ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-49901", QualityProfileRef: "comics", RootFolderRef: f.rf.Name,
		},
	}
	require.NoError(t, f.c.Create(ctx, comic))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Comic(comic.Name, f.ns).
		WithStatus(catalogac.ComicStatus().WithMetadata(catalogac.ComicMetadata().WithTitle("Saga").WithYear(2012))))
	require.NoError(t, err)
	issue := &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-00001.0", Namespace: f.ns},
		Spec:       catalogv1alpha1.IssueSpec{ComicRef: comic.Name, Number: "1", CalculatedNumberCentis: 100},
	}
	require.NoError(t, f.c.Create(ctx, issue))
	waitCached(t, ctx, f.c, comic, func() bool { return comic.Status.Metadata != nil })
	waitCached(t, ctx, f.c, issue, func() bool { return true })

	mustWriteFile(t, filepath.Join(f.root, "Saga", "Saga 001 (2012).cbz"), 2*smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Saga", "Saga 099 (2012).cbz"), 2*smallFile)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(2), got.FilesSeen)
	assert.Equal(t, int64(1), got.FilesMatched)
	u := unmatchedByPath(got)[filepath.Join("Saga", "Saga 099 (2012).cbz")]
	assert.Contains(t, u.Reason, "has no issue numbered")
	assert.Equal(t, []string{comic.Name}, u.Candidates)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: issue.Name}, mf.Spec.MediaRef)
	assert.Equal(t, "CBZ", mf.Spec.Quality.Name)
	assert.Equal(t, commonv1.ReleaseTypeIssue, mf.Spec.ReleaseType)
	assertSpecOnly(t, mf, "spec.mediaRef", "spec.path", "spec.quality", "spec.releaseType")

	var issues catalogv1alpha1.IssueList
	require.NoError(t, f.c.List(ctx, &issues, client.InNamespace(f.ns)))
	assert.Len(t, issues.Items, 1, "no Issue created for #99")
}

// An audiobook root matches an embedded ASIN to the existing Audiobook
// carrying it. An ASIN no audiobook has is unmatched -- the path does not
// say which marketplace it belongs to -- and no Audiobook is created.
func TestHandleMatchesAnEmbeddedASINAndNeverCreatesFromOne(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-audiobook", catalogv1alpha1.RootFolderKindAudiobook, "", catalogv1alpha1.ScanModeFull)

	ab := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "wizards-first-rule", Namespace: f.ns},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B002V0QK4C", QualityProfileRef: "audiobooks", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, ab))
	waitCached(t, ctx, f.c, ab, func() bool { return true })

	mustWriteFile(t, filepath.Join(f.root, "Terry Goodkind", "Wizard's First Rule [B002V0QK4C]", "01.mp3"), smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Terry Goodkind", "Stone of Tears [B00UNKNOWN]", "01.mp3"), smallFile)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesMatched)
	u := unmatchedByPath(got)[filepath.Join("Terry Goodkind", "Stone of Tears [B00UNKNOWN]", "01.mp3")]
	assert.Contains(t, u.Reason, "ASIN B00UNKNOWN")

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: ab.Name}, mf.Spec.MediaRef)
	assert.Equal(t, "MP3", mf.Spec.Quality.Name)
	assertSpecOnly(t, mf, "spec.mediaRef", "spec.quality")

	var abs catalogv1alpha1.AudiobookList
	require.NoError(t, f.c.List(ctx, &abs, client.InNamespace(f.ns)))
	assert.Len(t, abs.Items, 1, "an ASIN alone never creates an Audiobook")
}

// A book root attributes pkg/naming's "Author/Title/Author.epub" to the
// existing Book whose Author and title match -- a 1 MiB ebook that
// fsops.Walk alone would have skipped as a sample.
func TestHandleAttributesABookFileToAnExistingBookOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-book", catalogv1alpha1.RootFolderKindBook, "", catalogv1alpha1.ScanModeFull)

	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "frank-herbert", Namespace: f.ns},
		Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL79034A", QualityProfileRef: "books", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, author))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Author(author.Name, f.ns).
		WithStatus(catalogac.AuthorStatus().WithMetadata(catalogac.AuthorMetadata().WithName("Frank Herbert"))))
	require.NoError(t, err)
	authorRef := author.Name
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: f.ns},
		Spec:       catalogv1alpha1.BookSpec{AuthorRef: &authorRef, WorkID: "OL893415W"},
	}
	require.NoError(t, f.c.Create(ctx, book))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Book(book.Name, f.ns).
		WithStatus(catalogac.BookStatus().WithMetadata(catalogac.BookMetadata().WithTitle("Dune"))))
	require.NoError(t, err)
	waitCached(t, ctx, f.c, author, func() bool { return author.Status.Metadata != nil })
	waitCached(t, ctx, f.c, book, func() bool { return book.Status.Metadata != nil })

	mustWriteFile(t, filepath.Join(f.root, "Frank Herbert", "Dune", "Frank Herbert.epub"), smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Frank Herbert", "Children of Dune", "Frank Herbert.epub"), smallFile)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(2), got.FilesSeen)
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Len(t, got.Unmatched, 1)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: book.Name}, mf.Spec.MediaRef)
	assert.Equal(t, "EPUB", mf.Spec.Quality.Name)
	assertSpecOnly(t, mf, "spec.mediaRef", "spec.quality", "spec.releaseType")
}

// A re-scan of a file that already has a MediaFile -- here one the importer
// created, with a frozen score and provenance, and one catalogarr has since
// probed -- re-asserts every frozen field instead of releasing it. Server-
// side apply replaces a manager's owned set on every apply, and fileimport
// and rescan share importarr-worker, so an apply that omitted formatScore,
// matchedFormats, profileHash, importedFrom and original used to wipe them.
func TestHandleRescanReassertsFrozenImportFields(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-reassert", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-949", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))

	// The importer renamed the file; its name no longer carries the
	// release's quality or group.
	path := filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995).mkv")
	mustWriteFile(t, path, sampleFloor)

	name := k8s.ChildName(movie.Name, "mediafile", path)
	importedAt := metav1.NewTime(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	_, err := k8s.Apply(ctx, f.c, rescan.FieldManager, catalogac.MediaFile(name, f.ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}).
			WithPath(path).
			WithSizeBytes(1).
			WithQuality(commonv1.Quality{Name: "Remux-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierRemux}).
			WithReleaseGroup("FraMeSToR").
			WithFormatScore(1750).
			WithMatchedFormats("remux-tier-01").
			WithProfileHash("abc123").
			WithOriginal(true).
			WithImportedFrom(catalogac.ImportSource().WithDownloadRef("heat-dl").WithImportedAt(importedAt))))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, f.ns).
		WithStatus(catalogac.MediaFileStatus().WithProbeHash("probe-1")))
	require.NoError(t, err)
	waitFor(t, 10*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return f.c.List(ctx, &list, client.InNamespace(f.ns),
			client.MatchingFields{rescan.MediaFilePathIndexKey: path}) == nil &&
			len(list.Items) == 1 && list.Items[0].Status.ProbeHash == "probe-1"
	})

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Empty(t, got.Unmatched, "a file that already has a MediaFile is never re-matched")

	var after catalogv1alpha1.MediaFile
	waitFor(t, 10*time.Second, func() bool {
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &after) == nil &&
			after.Spec.SizeBytes == sampleFloor
	})
	assert.Equal(t, "Remux-1080p", after.Spec.Quality.Name, "the frozen quality is not re-parsed from the renamed file")
	assert.Equal(t, "FraMeSToR", after.Spec.ReleaseGroup)
	assert.Equal(t, int32(1750), after.Spec.FormatScore)
	assert.Equal(t, []string{"remux-tier-01"}, after.Spec.MatchedFormats)
	assert.Equal(t, "abc123", after.Spec.ProfileHash)
	require.NotNil(t, after.Spec.ImportedFrom)
	assert.Equal(t, "heat-dl", after.Spec.ImportedFrom.DownloadRef)
	assert.True(t, after.Spec.ImportedFrom.ImportedAt.Equal(&importedAt))
	require.NotNil(t, after.Spec.Original)
	assert.True(t, *after.Spec.Original)
	for _, leaf := range []string{"spec.formatScore", "spec.matchedFormats", "spec.profileHash", "spec.importedFrom.downloadRef", "spec.original"} {
		assert.Equal(t, string(rescan.FieldManager), managerFor(t, after.ManagedFields, "", leaf), leaf)
	}
	assert.Equal(t, "probe-1", after.Status.ProbeHash)
	assert.Equal(t, string(k8s.ManagerCatalogarr), managerFor(t, after.ManagedFields, "status", "status.probeHash"))
}

// assignScan creates a LibraryScan carrying the import-target annotation --
// what the UI's unmatched page creates to assign a file by hand -- and
// returns the task the LibraryScan controller would publish for it.
func (f *fixture) assignScan(t *testing.T, ctx context.Context, name, target, subpath string) (*catalogv1alpha1.LibraryScan, *fakeMessage) {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: f.ns,
			Annotations: map[string]string{"catalog.clustarr.io/import-target": target},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: f.rf.Name, Subpath: subpath, Mode: catalogv1alpha1.ScanModeFull},
	}
	require.NoError(t, f.c.Create(ctx, scan))
	waitCached(t, ctx, f.c, scan, func() bool { return true })
	task := f.taskForSubpath(subpath, false)
	task.LibraryScanRef.Name, task.LibraryScanRef.UID = scan.Name, string(scan.UID)
	task.Mode = string(catalogv1alpha1.ScanModeFull)
	return scan, newFakeMessage(t, task)
}

// Manual assignment: a LibraryScan whose subpath names one unmatched file and
// whose import-target names an item records that file against that item --
// the only way the never-guess rule lets an unmatchable file into the
// catalog, because a person made the attribution. The file's neighbour is not
// walked, the MediaFile records importedFrom.manual, and the split holds.
func TestHandleManualAssignmentRecordsOneFileAgainstTheNamedItem(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-assign", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	album := createArtistAlbum(t, ctx, f)

	rel := filepath.Join("Misc", "untitled", "track 1.flac")
	mustWriteFile(t, filepath.Join(f.root, rel), smallFile)
	mustWriteFile(t, filepath.Join(f.root, "Misc", "untitled", "track 2.flac"), smallFile)

	// The unmatched listing first, as the UI would see it.
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	require.Contains(t, unmatchedByPath(readProgress(t, ctx, f.bus, string(f.scan.UID))), rel)

	scan, msg := f.assignScan(t, ctx, "assign-1", "album/"+album.Name, rel)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	got := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(1), got.FilesSeen, "a file subpath walks exactly that file")
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Empty(t, got.Unmatched)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: album.Name}, mf.Spec.MediaRef)
	assert.Equal(t, filepath.Join(f.root, rel), mf.Spec.Path)
	require.NotNil(t, mf.Spec.ImportedFrom)
	assert.True(t, mf.Spec.ImportedFrom.Manual)
	assert.False(t, mf.Spec.ImportedFrom.ImportedAt.IsZero())
	assertSpecOnly(t, mf, "spec.mediaRef", "spec.path", "spec.importedFrom.manual")

	var albums catalogv1alpha1.AlbumList
	require.NoError(t, f.c.List(ctx, &albums, client.InNamespace(f.ns)))
	assert.Len(t, albums.Items, 1)
}

// Every way a manual assignment can be wrong is refused as the scan's
// outcome -- Done with the reason, which the LibraryScan controller reports
// as Failed -- and nothing is recorded. A path already recorded against
// another item is reported per file instead, because spec.mediaRef is
// immutable.
func TestHandleManualAssignmentRefusesWhatItCannotHonour(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-assign-bad", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	album := createArtistAlbum(t, ctx, f)

	rel := filepath.Join("Misc", "track.flac")
	mustWriteFile(t, filepath.Join(f.root, rel), smallFile)

	for i, tc := range []struct {
		target, subpath, want string
	}{
		{"album/Not A Name", rel, "invalid annotation"},
		{"album", rel, "invalid annotation"},
		{"movie/heat", rel, "is a music root"},
		{"album/no-such-album", rel, "does not exist"},
		{"album/" + album.Name, "../elsewhere", "outside root folder"},
	} {
		scan, msg := f.assignScan(t, ctx, "bad-"+string(rune('a'+i)), tc.target, tc.subpath)
		require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg), tc.target)
		got := readProgress(t, ctx, f.bus, string(scan.UID))
		assert.True(t, got.Done, tc.target)
		assert.Contains(t, got.Error, tc.want, tc.target)
		assert.Zero(t, got.FilesMatched, tc.target)
	}
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.c.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items, "a refused assignment records nothing")

	// Recorded elsewhere: the path already backs a different album.
	other := filepath.Join(f.root, rel)
	_, err := k8s.Apply(ctx, f.c, rescan.FieldManager, catalogac.MediaFile("prior", f.ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "some-other-album"}).
			WithPath(other).WithSizeBytes(1)))
	require.NoError(t, err)
	waitFor(t, 10*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return f.c.List(ctx, &list, client.InNamespace(f.ns),
			client.MatchingFields{rescan.MediaFilePathIndexKey: other}) == nil && len(list.Items) == 1
	})
	scan, msg := f.assignScan(t, ctx, "elsewhere", "album/"+album.Name, rel)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	got := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	u := unmatchedByPath(got)[rel]
	assert.Contains(t, u.Reason, "already records this path against album/some-other-album")

	var prior catalogv1alpha1.MediaFile
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "prior"}, &prior))
	assert.Equal(t, "some-other-album", prior.Spec.MediaRef.Name, "never re-pointed")
}

// Release years are read in UTC. metav1.Time decodes into the replica's
// local zone, so on a replica west of UTC an album released 1 January 00:30
// UTC reads as the previous December, and a folder correctly named for its
// year would fail the year match. The zone is swapped before the album is
// created, so the manager's informer decodes it west of UTC exactly as a
// replica there would.
func TestHandleReadsReleaseYearsInUTC(t *testing.T) {
	ctx := context.Background()
	prev := time.Local
	time.Local = time.FixedZone("UTC-8", -8*3600)
	t.Cleanup(func() { time.Local = prev })

	f := newFixture(t, ctx, "rw-utc", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "someone", Namespace: f.ns},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mbid-1", QualityProfileRef: "music", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, artist))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Artist(artist.Name, f.ns).
		WithStatus(catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Someone"))))
	require.NoError(t, err)
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "new-year", Namespace: f.ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist.Name, ReleaseGroupID: "rg-1"},
	}
	require.NoError(t, f.c.Create(ctx, album))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Album(album.Name, f.ns).
		WithStatus(catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("New Year").
			WithReleaseDate(metav1.NewTime(time.Date(1997, 1, 1, 0, 30, 0, 0, time.UTC))))))
	require.NoError(t, err)
	waitCached(t, ctx, f.c, artist, func() bool { return artist.Status.Metadata != nil })
	waitCached(t, ctx, f.c, album, func() bool { return album.Status.Metadata != nil })
	require.Equal(t, 1996, album.Status.Metadata.ReleaseDate.Year(), "the premise: decoded west of UTC it reads 1996")

	mustWriteFile(t, filepath.Join(f.root, "Someone", "New Year (1997)", "01.flac"), smallFile)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Empty(t, got.Unmatched, "a 1997 folder matches a 1 January 1997 UTC release")
	assert.Equal(t, int64(1), got.FilesMatched)
}
