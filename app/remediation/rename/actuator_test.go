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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec/specwrite"
	"github.com/mediactl/clustarr/app/remediation"
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
			got, _, err := a.rootFolderRef(t.Context(), c, mf)
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

	_, how, err := a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "r"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameAs{on: true, mode: specwrite.RenameWhole}, how, "renameFiles is read for a music root")

	_, how, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "ab"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameAs{}, how, "renameTranscoded is video-only, whatever spec.original says of an audiobook")

	_, how, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameAs{on: true, mode: specwrite.RenameInFolder}, how, "a transcoded movie file under renameTranscoded")

	_, how, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameAs{}, how, "an original movie file under renameTranscoded alone")
}

// TestRenameModeOrganizesANonVideoFileInsideItsContainer is design D19 at
// the actuator: a root with renameFiles and organizeFolders renames a
// non-video file with RenameOrganize, bounded by its container's folder from
// rootFolderRef's walk -- an Album's Artist's, a Book's Author's or its own,
// an Audiobook's own, an Issue's Comic's -- and only while that folder is
// known and strictly under the root (an Artist whose name has not arrived
// has the root itself as its folder). A video file, a root without the
// field, and the field without renameFiles never organize.
func TestRenameModeOrganizesANonVideoFileInsideItsContainer(t *testing.T) {
	organize := catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true), OrganizeFolders: ptr.To(true)}
	root := func(name, path string, kind catalogv1alpha1.RootFolderKind, naming catalogv1alpha1.NamingSpec) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{ObjectMeta: objMeta(name), Spec: catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind, Naming: naming}}
	}
	artist := func(name, rootRef, path string) *catalogv1alpha1.Artist {
		return &catalogv1alpha1.Artist{
			ObjectMeta: objMeta(name), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: name, RootFolderRef: rootRef},
			Status: catalogv1alpha1.ArtistStatus{Path: path},
		}
	}
	album := func(name, artistRef string) *catalogv1alpha1.Album {
		return &catalogv1alpha1.Album{ObjectMeta: objMeta(name), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: artistRef, ReleaseGroupID: name}}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		root("music", "/data/media/music", catalogv1alpha1.RootFolderKindMusic, organize),
		root("music-flat", "/data/media/music-flat", catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true)}),
		root("music-off", "/data/media/music-off", catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.NamingSpec{OrganizeFolders: ptr.To(true)}),
		root("books", "/data/media/books", catalogv1alpha1.RootFolderKindBook, organize),
		root("audiobooks", "/data/media/audiobooks", catalogv1alpha1.RootFolderKindAudiobook, organize),
		root("comics", "/data/media/comics", catalogv1alpha1.RootFolderKindComic, organize),
		root("movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie, organize),
		artist("nirvana", "music", "/data/media/music/Nirvana"),
		artist("unnamed", "music", "/data/media/music"),
		artist("unwritten", "music", ""),
		artist("flat", "music-flat", "/data/media/music-flat/Flat"),
		artist("off", "music-off", "/data/media/music-off/Off"),
		album("nevermind", "nirvana"), album("unnamed-album", "unnamed"), album("unwritten-album", "unwritten"),
		album("flat-album", "flat"), album("off-album", "off"),
		&catalogv1alpha1.Author{
			ObjectMeta: objMeta("pratchett"), Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books"},
			Status: catalogv1alpha1.AuthorStatus{Path: "/data/media/books/Terry Pratchett"},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: objMeta("guards"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w1"},
			Status: catalogv1alpha1.BookStatus{Path: "/data/media/books/stale"},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: objMeta("dune"), Spec: catalogv1alpha1.BookSpec{WorkID: "w3", RootFolderRef: ptr.To("books")},
			Status: catalogv1alpha1.BookStatus{Path: "/data/media/books/Dune"},
		},
		&catalogv1alpha1.Audiobook{
			ObjectMeta: objMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"},
			Status: catalogv1alpha1.AudiobookStatus{Path: "/data/media/audiobooks/Terry Pratchett/Guards! Guards!"},
		},
		&catalogv1alpha1.Comic{
			ObjectMeta: objMeta("saga"), Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "1", RootFolderRef: "comics"},
			Status: catalogv1alpha1.ComicStatus{Path: "/data/media/comics/Saga (2012)"},
		},
		&catalogv1alpha1.Issue{ObjectMeta: objMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
		&catalogv1alpha1.Movie{ObjectMeta: objMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
	).Build()
	a := New(Options{})
	organized := func(container string) renameAs {
		return renameAs{on: true, mode: specwrite.RenameOrganize, container: container, organizes: true}
	}
	for _, tc := range []struct {
		name string
		ref  commonv1.MediaRef
		want renameAs
	}{
		{
			"an album track inside its artist's folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "nevermind", Track: "r"},
			organized("/data/media/music/Nirvana"),
		},
		{
			"an author's book inside its author's folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"},
			organized("/data/media/books/Terry Pratchett"),
		},
		{
			"a standalone book inside its own folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"},
			organized("/data/media/books/Dune"),
		},
		{
			"an audiobook part inside its audiobook's folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
			organized("/data/media/audiobooks/Terry Pratchett/Guards! Guards!"),
		},
		{
			"an issue inside its comic's folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"},
			organized("/data/media/comics/Saga (2012)"),
		},
		{
			"an artist whose folder is the root itself",
			commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "unnamed-album", Track: "r"},
			renameAs{on: true, mode: specwrite.RenameWhole, organizes: true},
		},
		{
			"an artist that has written no folder",
			commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "unwritten-album", Track: "r"},
			renameAs{on: true, mode: specwrite.RenameWhole, organizes: true},
		},
		{
			"a root without organizeFolders",
			commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "flat-album", Track: "r"},
			renameAs{on: true, mode: specwrite.RenameWhole},
		},
		{
			"organizeFolders without renameFiles",
			commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "off-album", Track: "r"},
			renameAs{},
		},
		{
			"a video file",
			commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
			renameAs{on: true, mode: specwrite.RenameWhole},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := &catalogv1alpha1.MediaFile{ObjectMeta: objMeta("f"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: tc.ref, Path: "/data/x", Original: ptr.To(true)}}
			_, how, err := a.renameMode(t.Context(), c, mf)
			require.NoError(t, err)
			assert.Equal(t, tc.want, how)
		})
	}
}

// organizeFixture is a music root with renameFiles, an artist folder on
// disk with one track flat in it, and the MediaFile proposing the track's
// path toRel -- relative to the root, the album folder's track when empty --
// in a folder that does not exist yet, renameable as catalog leaves it;
// extra objects join the fake client.
func organizeFixture(t *testing.T, organize bool, toRel string, extra ...client.Object) (mf *catalogv1alpha1.MediaFile, c client.Client, from, to string) {
	t.Helper()
	lib := t.TempDir()
	artistDir := filepath.Join(lib, "Nirvana")
	require.NoError(t, os.MkdirAll(artistDir, 0o755))
	from = filepath.Join(artistDir, "01 Smells Like Teen Spirit.flac")
	if toRel == "" {
		toRel = filepath.Join("Nirvana", "Nevermind (1991)", "Nirvana - Nevermind - 01 - Smells Like Teen Spirit.flac")
	}
	to = filepath.Join(lib, toRel)
	require.NoError(t, os.WriteFile(from, []byte("flac frames"), 0o644))
	info, err := os.Stat(from)
	require.NoError(t, err)
	mf = &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nevermind-01", UID: "uid-nevermind-01"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "nevermind", Track: "rec-01"},
			Path:     from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime()),
		},
		Status: catalogv1alpha1.MediaFileStatus{
			Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: to},
			Conditions: []metav1.Condition{
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale"},
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed"},
			},
		},
	}
	objs := append([]client.Object{
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("music"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: lib, Kind: catalogv1alpha1.RootFolderKindMusic,
			Naming: catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true), OrganizeFolders: ptr.To(organize)},
		}},
		&catalogv1alpha1.Artist{
			ObjectMeta: objMeta("nirvana"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"},
			Status: catalogv1alpha1.ArtistStatus{Path: artistDir},
		},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("nevermind"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "nirvana", ReleaseGroupID: "rg"}},
		mf,
	}, extra...)
	c = fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithStatusSubresource(&catalogv1alpha1.MediaFile{}).WithObjects(objs...).Build()
	return mf, c, from, to
}

// TestTurningOrganizeFoldersOnRetriesAHeldFolderMove is design D19's
// "the actuator's remembered hold is keyed by the mode too": a flat track
// whose proposal is in an album folder is held while the root only renames
// files, and the hold stands on the next pass; once the root organizes
// folders the same proposal is tried again under RenameOrganize and the
// track moves into the album folder.
func TestTurningOrganizeFoldersOnRetriesAHeldFolderMove(t *testing.T) {
	mf, c, from, to := organizeFixture(t, false, "")
	rec := k8sevents.NewFakeRecorder(16)
	a := New(Options{Recorder: rec})
	env := &remediation.Env{Reader: c, APIReader: c}

	_, err := a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	held := withPrefix(drain(rec), "Normal Held")
	require.Len(t, held, 1)
	assert.Contains(t, held[0], "the root does not organize folders")
	_, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Empty(t, drain(rec), "the hold stands while nothing changes")

	var root catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: "music"}, &root))
	root.Spec.Naming.OrganizeFolders = ptr.To(true)
	require.NoError(t, c.Update(t.Context(), &root))

	_, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Len(t, withPrefix(drain(rec), "Normal Renamed"), 1, "the held proposal is retried under the new mode")
	assert.NoFileExists(t, from)
	assert.FileExists(t, to)
}

// TestAnOrganizedMoveWaitsForAScanOfTheFolderItMovesTo is design D19 with
// spec D6: a LibraryScan walking the folder the track is to move into --
// here a multi-disc release's medium folder, which neither holds the track's
// folder nor is a file in it -- holds the move, as one walking the folder
// the track is in does, since the scan could find the track there before
// the move is recorded.
func TestAnOrganizedMoveWaitsForAScanOfTheFolderItMovesTo(t *testing.T) {
	medium := filepath.Join("Nirvana", "Nevermind (1991)", "CD 02")
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: objMeta("nevermind-scan"),
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "music", Subpath: medium},
		Status:     catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning},
	}
	mf, c, from, to := organizeFixture(t, true, filepath.Join(medium, "Nirvana - Nevermind - 02 - In Bloom.flac"), scan)
	rec := k8sevents.NewFakeRecorder(16)
	a := New(Options{Recorder: rec})
	env := &remediation.Env{Reader: c, APIReader: c}

	wake, err := a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Equal(t, recheckAfter, wake)
	assert.Len(t, withPrefix(drain(rec), "Normal "+ReasonScanInProgress), 1)
	assert.FileExists(t, from)
	assert.NoDirExists(t, filepath.Dir(to))

	var done catalogv1alpha1.LibraryScan
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(scan), &done))
	done.Status.Phase = catalogv1alpha1.ScanPhaseCompleted
	require.NoError(t, c.Update(t.Context(), &done)) // the fake client keeps no status subresource for it
	_, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Len(t, withPrefix(drain(rec), "Normal Renamed"), 1, "the move runs once the scan has finished")
	assert.FileExists(t, to)
}

// TestAnOrganizedMoveLeavingTheContainerIsHeldAndSaysWhy: a proposal
// outside the artist's folder is held under RenameOrganize, and the Event
// names the folder the move would leave.
func TestAnOrganizedMoveLeavingTheContainerIsHeldAndSaysWhy(t *testing.T) {
	mf, c, from, outside := organizeFixture(t, true, filepath.Join("Foo Fighters", "Foo Fighters (1995)", "x.flac"))
	rec := k8sevents.NewFakeRecorder(16)
	a := New(Options{Recorder: rec})

	_, err := a.Act(t.Context(), &remediation.Env{Reader: c, APIReader: c}, c, mf)
	require.NoError(t, err)
	held := withPrefix(drain(rec), "Normal Held")
	require.Len(t, held, 1)
	assert.Contains(t, held[0], "the move would leave "+filepath.Dir(from))
	assert.FileExists(t, from)
	assert.NoDirExists(t, filepath.Dir(outside))
}

// TestAVacatedCollisionTargetIsRetriedAfterTheRecheckInterval is 2026-11-10
// design D11 at the actuator: a collision is remembered with its time, so
// the same proposal is not tried again within collisionRecheck -- a pass
// that changes nothing raises no second Warning -- and is once it has
// passed, when the file in the way may have moved on (an audiobook's tied
// sibling renamed to "(01)" and then, by its own pass, on to "(02)");
// until then the proposal was never retried before a restart. The
// collision asks for no pass of its own; a refused pass within the
// interval asks for the one that retries it. The interval is the test's
// own (10 s over a fake clock, the production minute asserted once) and
// the advances are literal, so a longer or never-expiring interval and the
// old no-expiry semantics fail it alike.
func TestAVacatedCollisionTargetIsRetriedAfterTheRecheckInterval(t *testing.T) {
	require.Equal(t, time.Minute, collisionRecheck, "the production interval (design D11: retried after a minute)")
	prev := collisionRecheck
	collisionRecheck = 10 * time.Second
	t.Cleanup(func() { collisionRecheck = prev })

	dir := t.TempDir()
	from, to := filepath.Join(dir, "Guards! Guards! (02).mp3"), filepath.Join(dir, "Guards! Guards! (01).mp3")
	require.NoError(t, os.WriteFile(from, []byte("part one"), 0o644))
	require.NoError(t, os.WriteFile(to, []byte("in the way"), 0o644))
	info, err := os.Stat(from)
	require.NoError(t, err)

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards-2", UID: "uid-guards-2"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
			Path:     from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime()),
		},
		Status: catalogv1alpha1.MediaFileStatus{
			Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: to},
			Conditions: []metav1.Condition{
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale"},
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: dir, Kind: catalogv1alpha1.RootFolderKindAudiobook, Naming: catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true)},
		}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		mf,
	).Build()
	rec := k8sevents.NewFakeRecorder(16)
	a := New(Options{Recorder: rec})
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	env := &remediation.Env{Reader: c, APIReader: c}

	wake, err := a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Zero(t, wake, "a collision asks for no pass of its own (a permanent one would pass every minute for good)")
	assert.Len(t, withPrefix(drain(rec), "Warning Collision"), 1)
	assert.FileExists(t, from)

	require.NoError(t, os.Remove(to))
	clock = clock.Add(5 * time.Second)
	wake, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Empty(t, drain(rec), "the same proposal is not tried again within the interval")
	assert.Equal(t, 5*time.Second, wake, "and the pass at the interval's end, which will try it, is asked for")
	assert.FileExists(t, from)

	clock = clock.Add(5 * time.Second)
	wake, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Zero(t, wake)
	assert.Len(t, withPrefix(drain(rec), "Normal Renamed"), 1, "the vacated target is taken once the interval has passed")
	assert.NoFileExists(t, from)
	assert.FileExists(t, to)
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(mf), &got))
	assert.Equal(t, to, got.Spec.Path)
}

// drain is every event rec holds.
func drain(rec *k8sevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func withPrefix(evs []string, prefix string) []string {
	var out []string
	for _, ev := range evs {
		if strings.HasPrefix(ev, prefix) {
			out = append(out, ev)
		}
	}
	return out
}
