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

package rename

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func objMeta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }

// TestRootFolderRefResolvesEveryKind is the actuator's root resolution
// table (2026-11-10 design D7, §5.5): a Movie's own root, an Episode's
// Series', an Album's Artist's, a Book's own override else its Author's, an
// Audiobook's own and an Issue's Comic's; "" for an item that is gone, a
// standalone Book with no root, and a kind the loop does not name.
func TestRootFolderRefResolvesEveryKind(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.Movie{ObjectMeta: objMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
		&catalogv1alpha1.Series{ObjectMeta: objMeta("firefly"), Spec: catalogv1alpha1.SeriesSpec{TvdbID: 78874, RootFolderRef: "tv"}},
		&catalogv1alpha1.Episode{ObjectMeta: objMeta("firefly-s01e01"), Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "firefly", SeasonNumber: 1, EpisodeNumber: 1}},
		&catalogv1alpha1.Artist{ObjectMeta: objMeta("boc"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("mhtrtc"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("orphan-album"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "gone", ReleaseGroupID: "rg2"}},
		&catalogv1alpha1.Author{ObjectMeta: objMeta("pratchett"), Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books"}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("guards"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w1"}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("guards-elsewhere"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w2", RootFolderRef: ptr.To("books2")}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("dune"), Spec: catalogv1alpha1.BookSpec{WorkID: "w3", RootFolderRef: ptr.To("books2")}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("rootless"), Spec: catalogv1alpha1.BookSpec{WorkID: "w4"}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		&catalogv1alpha1.Comic{ObjectMeta: objMeta("saga"), Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "1", RootFolderRef: "comics"}},
		&catalogv1alpha1.Issue{ObjectMeta: objMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
		&catalogv1alpha1.Issue{ObjectMeta: objMeta("orphan-issue"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "gone", Number: "001"}},
	).Build()
	a := New(Options{})
	for _, tc := range []struct {
		name string
		ref  commonv1.MediaRef
		want string
	}{
		{"a movie's own", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, "movies"},
		{"an episode's series'", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "firefly-s01e01"}, "tv"},
		{"an album's artist's", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec"}, "music"},
		{"an album whose artist is gone", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "orphan-album"}, ""},
		{"an author's book", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"}, "books"},
		{"an author's book overriding its root", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards-elsewhere"}, "books2"},
		{"a standalone book", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"}, "books2"},
		{"a standalone book without a root", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "rootless"}, ""},
		{"an audiobook's own", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}, "audiobooks"},
		{"an issue's comic's", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"}, "comics"},
		{"an issue whose comic is gone", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "orphan-issue"}, ""},
		{"an item that is gone", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "nope"}, ""},
		{"a kind the loop does not name", commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "boc"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := &catalogv1alpha1.MediaFile{ObjectMeta: objMeta("f"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: tc.ref, Path: "/data/x"}}
			got, err := a.rootFolderRef(t.Context(), c, mf)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRenameModeReadsRenameFilesForEveryKindAndRenameTranscodedForVideoOnly:
// renameFiles on a music root renames an album's track; renameTranscoded
// alone renames nothing but a video file the transcode controller
// transcoded, whatever spec.original says of a non-video file.
func TestRenameModeReadsRenameFilesForEveryKindAndRenameTranscodedForVideoOnly(t *testing.T) {
	roots := []client.Object{
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("music"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/music", Kind: catalogv1alpha1.RootFolderKindMusic, Naming: catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true)},
		}},
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/audiobooks", Kind: catalogv1alpha1.RootFolderKindAudiobook, Naming: catalogv1alpha1.NamingSpec{RenameTranscoded: true},
		}},
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("movies"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/movies", Kind: catalogv1alpha1.RootFolderKindMovie, Naming: catalogv1alpha1.NamingSpec{RenameTranscoded: true},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(append(roots,
		&catalogv1alpha1.Artist{ObjectMeta: objMeta("boc"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("mhtrtc"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg"}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("ab"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		&catalogv1alpha1.Movie{ObjectMeta: objMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
	)...).Build()
	a := New(Options{})
	file := func(ref commonv1.MediaRef, original bool) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: objMeta("f"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/x", Original: ptr.To(original)}}
	}

	_, mode, err := a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "r"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameWhole, mode, "renameFiles is read for a music root")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "ab"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameOff, mode, "renameTranscoded is video-only, whatever spec.original says of an audiobook")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameInFolder, mode, "a transcoded movie file under renameTranscoded")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameOff, mode, "an original movie file under renameTranscoded alone")
}
