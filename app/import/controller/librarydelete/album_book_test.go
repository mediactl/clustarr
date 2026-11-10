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
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The Album and standalone Book deletes of the 2026-11-10 non-video
// libraries design (§9.2, §9.3) over the 2026-09-30 library delete: an
// album's files live in its artist's folder, so a files delete removes the
// album's own track files and its own subfolder, never the artist's; a
// standalone book has a folder of its own under the root, an author's book
// none of its own and no delete of its own.

func musicRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "music"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMusic},
	}
}

func pinkFloyd(folder string) *catalogv1alpha1.Artist {
	return &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pink-floyd", UID: "pf-uid"},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "83d91898-7763-47d7-b03b-b92132375c47", QualityProfileRef: "lossless", RootFolderRef: "music"},
		Status:     catalogv1alpha1.ArtistStatus{Path: folder},
	}
}

func album(name, folder, mode string) *catalogv1alpha1.Album {
	a := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, UID: types.UID(name + "-uid")},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "pink-floyd", ReleaseGroupID: "rg-" + name},
		Status:     catalogv1alpha1.AlbumStatus{Path: folder},
	}
	if mode != "" {
		a.Annotations = map[string]string{catalogv1alpha1.AnnotationDelete: mode}
	}
	return a
}

func albumRef(name string) commonv1.MediaRef {
	return commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: name}
}

func reconcileKind(t *testing.T, c client.Client, kind commonv1.MediaKind, name string) error {
	t.Helper()
	r := &Reconciler{Client: c, kind: kindFor(kind)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: name}})
	return err
}

func gone(t *testing.T, c client.Client, key types.NamespacedName, obj client.Object) bool {
	t.Helper()
	return apierrors.IsNotFound(c.Get(context.Background(), key, obj))
}

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: "media", Name: name}
}

func deleteError(t *testing.T, c client.Client, key types.NamespacedName, obj client.Object) string {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), key, obj))
	return obj.GetAnnotations()[catalogv1alpha1.AnnotationDeleteError]
}

// A files delete of an album removes its own track files and its own
// subfolder, cover and all, and leaves the artist's folder with the
// artist's own files and its other albums; its MediaFiles and the Album go,
// the Artist and the other album's file stay.
func TestDeleteAlbumFilesRemovesItsTracksAndItsOwnFolderNeverTheArtists(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Pink Floyd")
	dsotm := filepath.Join(artist, "The Dark Side of the Moon (1973)")
	wywh := filepath.Join(artist, "Wish You Were Here (1975)")
	write(t, filepath.Join(artist, "artist.nfo"))
	write(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	write(t, filepath.Join(dsotm, "02 - Breathe.flac"))
	write(t, filepath.Join(dsotm, "cover.jpg"))
	write(t, filepath.Join(wywh, "01 - Shine On.flac"))
	one := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(dsotm, "01 - Speak to Me.flac"))
	two := mediaFile("dsotm-02", albumRef("dsotm"), filepath.Join(dsotm, "02 - Breathe.flac"))
	keep := mediaFile("wywh-01", albumRef("wywh"), filepath.Join(wywh, "01 - Shine On.flac"))
	c := newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", dsotm, catalogv1alpha1.DeleteFiles), album("wywh", wywh, ""), &one, &two, &keep)

	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))

	assert.NoDirExists(t, dsotm, "the album's own folder goes, its cover with it")
	assert.FileExists(t, filepath.Join(artist, "artist.nfo"), "the artist's folder stays")
	assert.FileExists(t, filepath.Join(wywh, "01 - Shine On.flac"), "the other album stays")
	assert.DirExists(t, root)
	assert.True(t, gone(t, c, key("dsotm-01"), &catalogv1alpha1.MediaFile{}))
	assert.True(t, gone(t, c, key("dsotm-02"), &catalogv1alpha1.MediaFile{}))
	assert.True(t, gone(t, c, key("dsotm"), &catalogv1alpha1.Album{}))
	require.NoError(t, c.Get(context.Background(), key("wywh-01"), &catalogv1alpha1.MediaFile{}))
	require.NoError(t, c.Get(context.Background(), key("pink-floyd"), &catalogv1alpha1.Artist{}), "the Artist is not the delete's")
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Empty(t, list.Items, "an album has no exclusion kind a reader honours")
}

// An album whose files lie straight in the artist's folder -- its
// status.path the artist's own -- has no folder of its own: only its files
// go, and the artist's folder stays even when they were all it held. The
// same holds for an album whose status.path is not on disk and whose files
// share the artist's folder.
func TestDeleteAlbumWithFilesInTheArtistsFolderRemovesOnlyItsFiles(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Pink Floyd")
	write(t, filepath.Join(artist, "01 - Speak to Me.flac"))
	write(t, filepath.Join(artist, "02 - Breathe.flac"))
	one := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(artist, "01 - Speak to Me.flac"))
	two := mediaFile("dsotm-02", albumRef("dsotm"), filepath.Join(artist, "02 - Breathe.flac"))
	c := newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", artist, catalogv1alpha1.DeleteFiles), &one, &two)

	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))

	assert.NoFileExists(t, filepath.Join(artist, "01 - Speak to Me.flac"))
	assert.NoFileExists(t, filepath.Join(artist, "02 - Breathe.flac"))
	assert.DirExists(t, artist, "the artist's folder is never removed, emptied or not")
	assert.True(t, gone(t, c, key("dsotm"), &catalogv1alpha1.Album{}))

	write(t, filepath.Join(artist, "01 - Speak to Me.flac"))
	write(t, filepath.Join(artist, "artist.nfo"))
	one = mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(artist, "01 - Speak to Me.flac"))
	c = newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", filepath.Join(artist, "Not Here (1973)"), catalogv1alpha1.DeleteFiles), &one)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))
	assert.NoFileExists(t, filepath.Join(artist, "01 - Speak to Me.flac"))
	assert.FileExists(t, filepath.Join(artist, "artist.nfo"), "the files' shared folder is the artist's, which is no folder of the album's")
}

// An album whose status.path is not on disk is deleted from the folder its
// files share, when that folder is strictly under the artist's -- the
// preset's name against the scanned one, as for a movie.
func TestDeleteAlbumWhoseResolvedFolderIsNotOnDiskUsesItsFilesFolder(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Pink Floyd")
	real := filepath.Join(artist, "1973 - DSOTM")
	write(t, filepath.Join(real, "01.flac"))
	write(t, filepath.Join(real, "cover.jpg"))
	write(t, filepath.Join(artist, "artist.nfo"))
	one := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(real, "01.flac"))
	c := newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", filepath.Join(artist, "The Dark Side of the Moon (1973)"), catalogv1alpha1.DeleteFiles), &one)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))
	assert.NoDirExists(t, real)
	assert.FileExists(t, filepath.Join(artist, "artist.nfo"))
}

// An album's folder holding another item's MediaFile refuses the delete:
// nothing is removed and the reason names the file.
func TestDeleteAlbumRefusesAFolderHoldingAnotherAlbumsFile(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Pink Floyd")
	dsotm := filepath.Join(artist, "The Dark Side of the Moon (1973)")
	write(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	write(t, filepath.Join(dsotm, "stray.flac"))
	one := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(dsotm, "01 - Speak to Me.flac"))
	other := mediaFile("wywh-01", albumRef("wywh"), filepath.Join(dsotm, "stray.flac"))
	c := newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", dsotm, catalogv1alpha1.DeleteFiles), album("wywh", "", ""), &one, &other)

	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"), "a refusal is not retried")

	assert.FileExists(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	require.NoError(t, c.Get(context.Background(), key("dsotm-01"), &catalogv1alpha1.MediaFile{}))
	var a catalogv1alpha1.Album
	assert.Contains(t, deleteError(t, c, key("dsotm"), &a), "wywh-01")
	assert.Equal(t, catalogv1alpha1.DeleteFiles, a.Annotations[catalogv1alpha1.AnnotationDelete], "the request stays")
}

// A records delete leaves the disk alone and removes the MediaFiles and
// the Album; with the Artist gone a files delete is refused, since nothing
// says what the album's root is, while a records delete still goes.
func TestDeleteAlbumRecordsLeavesTheDiskAloneAndNeedsNoArtist(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Pink Floyd")
	dsotm := filepath.Join(artist, "The Dark Side of the Moon (1973)")
	write(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	one := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(dsotm, "01 - Speak to Me.flac"))
	c := newClient(t, musicRoot(root), pinkFloyd(artist), album("dsotm", dsotm, catalogv1alpha1.DeleteRecords), &one)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))
	assert.FileExists(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	assert.True(t, gone(t, c, key("dsotm-01"), &catalogv1alpha1.MediaFile{}))
	assert.True(t, gone(t, c, key("dsotm"), &catalogv1alpha1.Album{}))

	one = mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(dsotm, "01 - Speak to Me.flac"))
	c = newClient(t, musicRoot(root), album("dsotm", dsotm, catalogv1alpha1.DeleteFiles), &one)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))
	assert.FileExists(t, filepath.Join(dsotm, "01 - Speak to Me.flac"))
	assert.Contains(t, deleteError(t, c, key("dsotm"), &catalogv1alpha1.Album{}), "root folder is unknown")

	c = newClient(t, musicRoot(root), album("dsotm", dsotm, catalogv1alpha1.DeleteRecords), &one)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindAlbum, "dsotm"))
	assert.True(t, gone(t, c, key("dsotm"), &catalogv1alpha1.Album{}))
}

func booksRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "books"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindBook},
	}
}

func standaloneBook(folder, mode string) *catalogv1alpha1.Book {
	profile, root := "ebook", "books"
	return &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "media", Name: "dispossessed", UID: "dis-uid",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: mode},
		},
		Spec:   catalogv1alpha1.BookSpec{WorkID: "OL1W", QualityProfileRef: &profile, RootFolderRef: &root},
		Status: catalogv1alpha1.BookStatus{Path: folder},
	}
}

var dispossessed = commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dispossessed"}

// A standalone book's files delete removes its own folder under the root
// (app/catalog/controller/book.Path since 2026-11-10), its MediaFile and
// the Book; the root stays.
func TestDeleteStandaloneBookFilesRemovesItsFolder(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "The Dispossessed")
	write(t, filepath.Join(folder, "The Dispossessed.epub"))
	write(t, filepath.Join(folder, "cover.jpg"))
	write(t, filepath.Join(root, "Other Book", "other.epub"))
	mf := mediaFile("dis-file", dispossessed, filepath.Join(folder, "The Dispossessed.epub"))
	c := newClient(t, booksRoot(root), standaloneBook(folder, catalogv1alpha1.DeleteFiles), &mf)

	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))

	assert.NoDirExists(t, folder)
	assert.DirExists(t, root, "the RootFolder is never removed")
	assert.FileExists(t, filepath.Join(root, "Other Book", "other.epub"))
	assert.True(t, gone(t, c, key("dis-file"), &catalogv1alpha1.MediaFile{}))
	assert.True(t, gone(t, c, key("dispossessed"), &catalogv1alpha1.Book{}))
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Empty(t, list.Items, "a book has no exclusion kind a reader honours")
}

// A standalone book written before 2026-11-10 carries the root itself as
// status.path: that is no folder of the book's, so the delete removes the
// folder its file is in, whole, and a file straight in the root goes
// alone.
func TestDeleteStandaloneBookWhosePathIsTheRootUsesItsFilesFolder(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "The Dispossessed")
	write(t, filepath.Join(folder, "The Dispossessed.epub"))
	write(t, filepath.Join(folder, "metadata.opf"))
	write(t, filepath.Join(root, "other.nfo"))
	mf := mediaFile("dis-file", dispossessed, filepath.Join(folder, "The Dispossessed.epub"))
	c := newClient(t, booksRoot(root), standaloneBook(root, catalogv1alpha1.DeleteFiles), &mf)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))
	assert.NoDirExists(t, folder)
	assert.FileExists(t, filepath.Join(root, "other.nfo"), "the root is left as it was")
	assert.True(t, gone(t, c, key("dispossessed"), &catalogv1alpha1.Book{}))

	flat := filepath.Join(root, "The Dispossessed.epub")
	write(t, flat)
	mf = mediaFile("dis-file", dispossessed, flat)
	c = newClient(t, booksRoot(root), standaloneBook(root, catalogv1alpha1.DeleteFiles), &mf)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))
	assert.NoFileExists(t, flat)
	assert.FileExists(t, filepath.Join(root, "other.nfo"))
	assert.DirExists(t, root)
}

// A standalone book's folder holding another item's file refuses; a
// records delete leaves the disk alone.
func TestDeleteStandaloneBookRefusesAnotherItemsFileAndRecordsKeepsTheDisk(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "The Dispossessed")
	write(t, filepath.Join(folder, "The Dispossessed.epub"))
	write(t, filepath.Join(folder, "stray.epub"))
	mf := mediaFile("dis-file", dispossessed, filepath.Join(folder, "The Dispossessed.epub"))
	other := mediaFile("stray-file", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "stray"}, filepath.Join(folder, "stray.epub"))
	c := newClient(t, booksRoot(root), standaloneBook(folder, catalogv1alpha1.DeleteFiles), &mf, &other)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))
	assert.FileExists(t, filepath.Join(folder, "The Dispossessed.epub"))
	assert.Contains(t, deleteError(t, c, key("dispossessed"), &catalogv1alpha1.Book{}), "stray-file")

	c = newClient(t, booksRoot(root), standaloneBook(folder, catalogv1alpha1.DeleteRecords), &mf, &other)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))
	assert.FileExists(t, filepath.Join(folder, "The Dispossessed.epub"))
	assert.True(t, gone(t, c, key("dis-file"), &catalogv1alpha1.MediaFile{}))
	assert.True(t, gone(t, c, key("dispossessed"), &catalogv1alpha1.Book{}))
	require.NoError(t, c.Get(context.Background(), key("stray-file"), &catalogv1alpha1.MediaFile{}))
}

// An author's book is deleted with its author (design §9.3): the request
// is refused on the Book, naming the author, and nothing moves -- not the
// book's file, which sits in the author's folder, nor the records.
func TestAnAuthorsBookIsRefused(t *testing.T) {
	root := t.TempDir()
	authorFolder := filepath.Join(root, "Le Guin")
	write(t, filepath.Join(authorFolder, "The Dispossessed", "Le Guin.epub"))
	author := "le-guin"
	b := standaloneBook(filepath.Join(authorFolder, "The Dispossessed"), catalogv1alpha1.DeleteFiles)
	b.Spec.AuthorRef = &author
	mf := mediaFile("dis-file", dispossessed, filepath.Join(authorFolder, "The Dispossessed", "Le Guin.epub"))
	c := newClient(t, booksRoot(root), b, &mf)

	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"), "a refusal is not retried")

	assert.FileExists(t, filepath.Join(authorFolder, "The Dispossessed", "Le Guin.epub"))
	require.NoError(t, c.Get(context.Background(), key("dis-file"), &catalogv1alpha1.MediaFile{}))
	var got catalogv1alpha1.Book
	assert.Contains(t, deleteError(t, c, key("dispossessed"), &got), "deleted with its author (le-guin)")
	assert.Equal(t, catalogv1alpha1.DeleteFiles, got.Annotations[catalogv1alpha1.AnnotationDelete], "the request stays, for Cancel")

	b.Annotations[catalogv1alpha1.AnnotationDelete] = catalogv1alpha1.DeleteRecords
	c = newClient(t, booksRoot(root), b, &mf)
	require.NoError(t, reconcileKind(t, c, commonv1.MediaKindBook, "dispossessed"))
	require.NoError(t, c.Get(context.Background(), key("dispossessed"), &got), "records too: the author's rollup owns the book")
}

// Check refuses a folder at or above the Keep folder it is a part of, and
// RemoveFromDisk prunes no higher than that folder, even when it is left
// empty; a Keep outside the Root is no bound and the Root bounds instead.
func TestKeepBoundsTheCheckAndThePrune(t *testing.T) {
	root := filepath.Join(t.TempDir(), "music")
	artist := filepath.Join(root, "Pink Floyd")
	dsotm := filepath.Join(artist, "DSOTM")
	mf := mediaFile("dsotm-01", albumRef("dsotm"), filepath.Join(dsotm, "01.flac"))
	tgt := Target{Root: root, Folder: dsotm, Keep: artist, Keys: map[string]bool{TargetKey(commonv1.MediaKindAlbum, "dsotm"): true}, Files: []catalogv1alpha1.MediaFile{mf}}
	require.NoError(t, Check(tgt, tgt.Files, nil))

	at := tgt
	at.Folder = artist
	err := Check(at, at.Files, nil)
	require.True(t, errors.Is(err, ErrRefused))
	assert.Contains(t, err.Error(), "the folder it shares")
	above := tgt
	above.Folder = root
	require.True(t, errors.Is(Check(above, above.Files, nil), ErrRefused))

	write(t, filepath.Join(dsotm, "01.flac"))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoDirExists(t, dsotm)
	assert.DirExists(t, artist, "pruned no higher than Keep, though it is empty")

	outside := Target{Root: root, Keep: filepath.Dir(root)}
	assert.Equal(t, root, outside.keep(), "a Keep outside the Root is no bound")
	assert.Equal(t, root, Target{Root: root}.keep())
	assert.Equal(t, artist, tgt.keep())
}
