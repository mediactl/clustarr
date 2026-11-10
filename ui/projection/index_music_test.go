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

package projection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// musicFixtures are an artist with two albums (the older listed second in
// the fixture so the index's ordering shows), an author with an audiobook
// linked by authorRef, an audiobook with no authorRef, and one file each of
// kind album and audiobook.
func musicFixtures() []client.Object {
	authorRef := "author-1"
	return []client.Object{
		&catalogv1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "artist-1", Namespace: "default", UID: "a1"},
			Spec:       catalogv1.ArtistSpec{MusicBrainzID: "mb-artist-1", QualityProfileRef: "p", RootFolderRef: "r"},
		},
		&catalogv1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "album-new", Namespace: "default", UID: "al-new"},
			Spec:       catalogv1.AlbumSpec{ArtistRef: "artist-1", ReleaseGroupID: "rg-new"},
			Status: catalogv1.AlbumStatus{Metadata: &catalogv1.AlbumMetadata{
				Title: "New", ReleaseDate: &metav1.Time{Time: metav1.Now().AddDate(-1, 0, 0)},
			}},
		},
		&catalogv1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "album-old", Namespace: "default", UID: "al-old"},
			Spec:       catalogv1.AlbumSpec{ArtistRef: "artist-1", ReleaseGroupID: "rg-old"},
			Status: catalogv1.AlbumStatus{Metadata: &catalogv1.AlbumMetadata{
				Title: "Old", ReleaseDate: &metav1.Time{Time: metav1.Now().AddDate(-10, 0, 0)},
			}},
		},
		&catalogv1.Album{
			// Another namespace's album naming the same artistRef is not
			// the artist's: refs resolve within a namespace.
			ObjectMeta: metav1.ObjectMeta{Name: "album-elsewhere", Namespace: "other", UID: "al-other"},
			Spec:       catalogv1.AlbumSpec{ArtistRef: "artist-1", ReleaseGroupID: "rg-other"},
		},
		&catalogv1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: "author-1", Namespace: "default", UID: "au1"},
			Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: "p", RootFolderRef: "r"},
		},
		&catalogv1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "book-linked", Namespace: "default", UID: "ab-linked"},
			Spec:       catalogv1.AudiobookSpec{ASIN: "B000LINKED", AuthorRef: &authorRef, QualityProfileRef: "p", RootFolderRef: "r"},
		},
		&catalogv1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "book-unlinked", Namespace: "default", UID: "ab-unlinked"},
			Spec:       catalogv1.AudiobookSpec{ASIN: "B000UNLINKED", QualityProfileRef: "p", RootFolderRef: "r"},
			Status: catalogv1.AudiobookStatus{Metadata: &catalogv1.AudiobookMetadata{
				Title: "Alone", Authors: []catalogv1.NamedRef{{Name: "Ines Varga"}},
			}},
		},
		&catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "track-file", Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "album-new", Track: "rec-1"},
				Path:     "/data/media/music/Artist/New/01 - One.flac",
			},
		},
		&catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "part-file", Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "book-linked"},
				Path:     "/data/media/audiobooks/Author/Book/Book - Part 01.m4b",
			},
		},
	}
}

// TestIndexMusicLookups is design §7.5: under IndexOptions.Music the index
// lists Artists, Albums, Authors and Audiobooks, resolves them by UID, MBID
// and ASIN, buckets albums by artist (release date first) and audiobooks by
// author or, without an authorRef, by their first author's name, and
// answers the file-name rule for album and audiobook files.
func TestIndexMusicLookups(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(musicFixtures()...).Build()
	idx, err := projection.BuildIndexWith(context.Background(), c, projection.IndexOptions{Music: true})
	require.NoError(t, err)
	require.True(t, idx.Music())

	for _, uid := range []types.UID{"a1", "al-new", "au1", "ab-linked", "ab-unlinked"} {
		_, ok := idx.ByUID(uid)
		require.True(t, ok, "ByUID(%s)", uid)
	}
	artist, ok := idx.ArtistByUID("a1")
	require.True(t, ok)
	require.Equal(t, "artist-1", artist.Name)
	_, ok = idx.AlbumByUID("al-new")
	require.True(t, ok)
	_, ok = idx.AuthorByUID("au1")
	require.True(t, ok)
	_, ok = idx.AudiobookByUID("ab-linked")
	require.True(t, ok)

	obj, ok := idx.ByMBID(commonv1.MediaKindArtist, "mb-artist-1")
	require.True(t, ok)
	require.Equal(t, types.UID("a1"), obj.GetUID())
	obj, ok = idx.ByMBID(commonv1.MediaKindAlbum, "rg-old")
	require.True(t, ok)
	require.Equal(t, types.UID("al-old"), obj.GetUID())
	_, ok = idx.ByMBID(commonv1.MediaKindAlbum, "mb-artist-1")
	require.False(t, ok, "an artist id is no release group")
	_, ok = idx.ByMBID(commonv1.MediaKindMovie, "mb-artist-1")
	require.False(t, ok)
	ab, ok := idx.ByASIN("B000UNLINKED")
	require.True(t, ok)
	require.Equal(t, types.UID("ab-unlinked"), ab.UID)
	_, ok = idx.ByASIN("B000NOPE")
	require.False(t, ok)

	albums := idx.AlbumsOf("a1")
	require.Len(t, albums, 2, "the other namespace's album is not the artist's")
	require.Equal(t, []string{"album-old", "album-new"}, []string{albums[0].Name, albums[1].Name}, "oldest first")
	owner, ok := idx.ArtistOfAlbum("al-new")
	require.True(t, ok)
	require.Equal(t, types.UID("a1"), owner.UID)
	_, ok = idx.ArtistOfAlbum("al-other")
	require.False(t, ok)

	books := idx.AudiobooksOf("au1")
	require.Len(t, books, 1)
	require.Equal(t, "book-linked", books[0].Name)
	author, ok := idx.AuthorOfAudiobook("ab-linked")
	require.True(t, ok)
	require.Equal(t, types.UID("au1"), author.UID)
	_, ok = idx.AuthorOfAudiobook("ab-unlinked")
	require.False(t, ok)
	unlinked := idx.UnlinkedAudiobooks()
	require.Len(t, unlinked["Ines Varga"], 1)
	require.Equal(t, "book-unlinked", unlinked["Ines Varga"][0].Name)
	require.Equal(t, "Ines Varga", projection.FirstAuthorName(unlinked["Ines Varga"][0]))

	al, ok := idx.AlbumByName("default", "album-new")
	require.True(t, ok)
	require.Equal(t, types.UID("al-new"), al.UID)
	_, ok = idx.AlbumByName("other", "album-new")
	require.False(t, ok, "names are per namespace")
	_, ok = idx.ArtistByName("default", "artist-1")
	require.True(t, ok)
	_, ok = idx.AuthorByName("default", "author-1")
	require.True(t, ok)
	_, ok = idx.AudiobookByName("default", "book-linked")
	require.True(t, ok)

	names := func(fs []*catalogv1.MediaFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	require.Equal(t, []string{"track-file"}, names(idx.FilesEndingWith("New/01 - One.flac")))
	require.Equal(t, []string{"part-file"}, names(idx.FilesEndingWith("Book/Book - Part 01.m4b")))
	f, ok := idx.MediaFileByName("default", "track-file")
	require.True(t, ok)
	require.Equal(t, "rec-1", f.Spec.MediaRef.Track)
}

// TestIndexWithoutMusicListsNoMusic: BuildIndex (the roots off) builds none
// of the music lookups -- a cluster with the flag off pays nothing -- and
// every one answers "not found", the album and audiobook files included.
func TestIndexWithoutMusicListsNoMusic(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(musicFixtures()...).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)
	require.False(t, idx.Music())

	_, ok := idx.ByUID("a1")
	require.False(t, ok)
	_, ok = idx.ByMBID(commonv1.MediaKindArtist, "mb-artist-1")
	require.False(t, ok)
	_, ok = idx.ByASIN("B000LINKED")
	require.False(t, ok)
	require.Empty(t, idx.Artists())
	require.Empty(t, idx.Albums())
	require.Empty(t, idx.Authors())
	require.Empty(t, idx.Audiobooks())
	require.Empty(t, idx.AlbumsOf("a1"))
	require.Empty(t, idx.UnlinkedAudiobooks())
	require.Empty(t, idx.FilesEndingWith("New/01 - One.flac"))
	_, ok = idx.MediaFileByName("default", "track-file")
	require.False(t, ok)
}
