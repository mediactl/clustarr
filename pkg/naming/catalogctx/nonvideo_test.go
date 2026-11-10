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

package catalogctx_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func rootAt(p string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{
		Path: p, Kind: kind, Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectPlex},
	}}
}

func utcDate(y int, m time.Month, d int) *metav1.Time {
	t := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return &t
}

// TestTrackFilePath is the album table: the track preset's file under the
// album folder, which is the Album's status.path when resolved, else the
// artist folder (status.path, spec.folder or the preset) and the album
// preset's own segment.
func TestTrackFilePath(t *testing.T) {
	root := rootAt("/data/music", catalogv1alpha1.RootFolderKindMusic)
	artist := func(statusPath string, folder *string) *catalogv1alpha1.Artist {
		return &catalogv1alpha1.Artist{
			Spec:   catalogv1alpha1.ArtistSpec{MusicBrainzID: "69158f97-4c07-4c4e-baf8-4e4ab1ed666e", Folder: folder},
			Status: catalogv1alpha1.ArtistStatus{Path: statusPath, Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Boards of Canada"}},
		}
	}
	album := func(statusPath string) *catalogv1alpha1.Album {
		return &catalogv1alpha1.Album{
			Spec: catalogv1alpha1.AlbumSpec{ReleaseGroupID: "rg-1"},
			Status: catalogv1alpha1.AlbumStatus{Path: statusPath, Metadata: &catalogv1alpha1.AlbumMetadata{
				Title: "Music Has the Right to Children", ReleaseDate: utcDate(1998, time.April, 20),
			}},
		}
	}
	track := &catalogv1alpha1.Track{RecordingID: "rec-3", Medium: 1, Number: 3, Title: "Telephasic Workshop"}
	const file = "Boards of Canada - Music Has the Right to Children - 03 - Telephasic Workshop.flac"

	for _, tc := range []struct {
		name   string
		artist *catalogv1alpha1.Artist
		album  *catalogv1alpha1.Album
		want   string
	}{
		{"every folder from the presets", artist("", nil), album(""),
			"/data/music/Boards of Canada/Music Has the Right to Children (1998)/" + file},
		{"the artist's spec.folder", artist("", ptr.To("BoC")), album(""),
			"/data/music/BoC/Music Has the Right to Children (1998)/" + file},
		{"the artist's resolved path", artist("/data/music/Boards of Canada (band)", nil), album(""),
			"/data/music/Boards of Canada (band)/Music Has the Right to Children (1998)/" + file},
		{"the album's resolved path wins", artist("/data/music/Boards of Canada", nil), album("/data/music/BoC/MHTRTC"),
			"/data/music/BoC/MHTRTC/" + file},
	} {
		c, ok := catalogctx.Album(tc.artist, tc.album, track)
		require.True(t, ok, tc.name)
		assert.Equal(t, 3, c.Track, tc.name)
		assert.Equal(t, 1, c.Disc, tc.name)
		assert.Equal(t, 1998, c.Year, tc.name)
		got, err := catalogctx.TrackFilePath(root, tc.artist, tc.album, c, ".flac")
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}

	_, ok := catalogctx.Album(artist("", nil), &catalogv1alpha1.Album{}, track)
	assert.False(t, ok, "no album metadata")
	_, ok = catalogctx.Album(&catalogv1alpha1.Artist{}, album(""), track)
	assert.False(t, ok, "no artist name: the file preset names the artist")
	_, ok = catalogctx.Album(artist("", nil), album(""), nil)
	assert.False(t, ok, "no track")

	_, err := catalogctx.TrackFilePath(root, artist("", nil), album("/data/movies/elsewhere"), mustAlbum(t, artist("", nil), album(""), track), ".flac")
	require.Error(t, err, "an album resolved under another root is not renamed into it")
}

func mustAlbum(t *testing.T, a *catalogv1alpha1.Artist, al *catalogv1alpha1.Album, tr *catalogv1alpha1.Track) naming.Context {
	t.Helper()
	c, ok := catalogctx.Album(a, al, tr)
	require.True(t, ok)
	return c
}

// TestBookFilePath is the book table: Readarr's "{Book Title}/{Author
// Name}" under the author folder for an Author's book, and "{Book
// Title}/{Book Title}" under the root for a standalone one, as the import
// places them.
func TestBookFilePath(t *testing.T) {
	root := rootAt("/data/books", catalogv1alpha1.RootFolderKindBook)
	author := func(statusPath string, folder *string) *catalogv1alpha1.Author {
		return &catalogv1alpha1.Author{
			Spec:   catalogv1alpha1.AuthorSpec{Folder: folder},
			Status: catalogv1alpha1.AuthorStatus{Path: statusPath, Metadata: &catalogv1alpha1.AuthorMetadata{Name: "Terry Pratchett"}},
		}
	}
	book := &catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{
		Title: "Guards! Guards!", ReleaseDate: utcDate(1989, time.August, 1),
	}}}

	for _, tc := range []struct {
		name   string
		author *catalogv1alpha1.Author
		want   string
	}{
		{"an author's book, folders from the presets", author("", nil), "/data/books/Terry Pratchett/Guards! Guards!/Terry Pratchett.epub"},
		{"the author's spec.folder", author("", ptr.To("Pratchett, Terry")), "/data/books/Pratchett, Terry/Guards! Guards!/Terry Pratchett.epub"},
		{"the author's resolved path", author("/data/books/T. Pratchett", nil), "/data/books/T. Pratchett/Guards! Guards!/Terry Pratchett.epub"},
		{"a standalone book", nil, "/data/books/Guards! Guards!/Guards! Guards!.epub"},
	} {
		c, ok := catalogctx.Book(tc.author, book)
		require.True(t, ok, tc.name)
		assert.Equal(t, 1989, c.Year, tc.name)
		got, err := catalogctx.BookFilePath(root, tc.author, c, ".epub")
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}

	_, ok := catalogctx.Book(nil, &catalogv1alpha1.Book{})
	assert.False(t, ok, "no book metadata")
	_, ok = catalogctx.Book(&catalogv1alpha1.Author{}, book)
	assert.False(t, ok, "an author's book waits for the author's name")
}

// TestAudiobookFilePath is the audiobook table: the folder preset's last
// segment as the file name, the part in brackets for one of several, and
// the Audiobook's status.path or spec.folder as the folder when set.
func TestAudiobookFilePath(t *testing.T) {
	root := rootAt("/data/audiobooks", catalogv1alpha1.RootFolderKindAudiobook)
	ab := func(statusPath string, folder *string, parts int32) *catalogv1alpha1.Audiobook {
		return &catalogv1alpha1.Audiobook{
			Spec: catalogv1alpha1.AudiobookSpec{Folder: folder},
			Status: catalogv1alpha1.AudiobookStatus{Path: statusPath, PartCount: parts, Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title:       "Guards! Guards!",
				Authors:     []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
				Narrators:   []string{"Nigel Planer"},
				Series:      &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
				ReleaseDate: utcDate(1989, time.August, 1),
			}},
		}
	}
	const folder = "Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer"

	for _, tc := range []struct {
		name string
		ab   *catalogv1alpha1.Audiobook
		part int32
		want string
	}{
		{"one file", ab("", nil, 0), 0, "/data/audiobooks/" + folder + "/8 - 1989 - Guards! Guards! Nigel Planer.m4b"},
		{"part 1 of 12", ab("", nil, 12), 1, "/data/audiobooks/" + folder + "/8 - 1989 - Guards! Guards! Nigel Planer (01).m4b"},
		{"part 12 of 12", ab("", nil, 12), 12, "/data/audiobooks/" + folder + "/8 - 1989 - Guards! Guards! Nigel Planer (12).m4b"},
		{"spec.folder", ab("", ptr.To("Pratchett/Guards"), 2), 2, "/data/audiobooks/Pratchett/Guards/8 - 1989 - Guards! Guards! Nigel Planer (02).m4b"},
		{"resolved path", ab("/data/audiobooks/Discworld 08", nil, 0), 0, "/data/audiobooks/Discworld 08/8 - 1989 - Guards! Guards! Nigel Planer.m4b"},
	} {
		c, ok := catalogctx.Audiobook(tc.ab, tc.part)
		require.True(t, ok, tc.name)
		assert.Equal(t, int(tc.part), c.PartNumber, tc.name)
		assert.Equal(t, int(tc.ab.Status.PartCount), c.PartCount, tc.name)
		got, err := catalogctx.AudiobookFilePath(root, tc.ab, c, ".m4b")
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}

	_, ok := catalogctx.Audiobook(&catalogv1alpha1.Audiobook{}, 1)
	assert.False(t, ok, "no metadata")
}

// TestIssueFilePath is the issue table: "{Comic Series Title} c{issue}"
// under the comic folder, the special version after the number for a
// collected edition and nothing for a normal run or volumes-as-issues.
func TestIssueFilePath(t *testing.T) {
	root := rootAt("/data/comics", catalogv1alpha1.RootFolderKindComic)
	comic := func(statusPath string, folder *string, v catalogv1alpha1.ComicSpecialVersion) *catalogv1alpha1.Comic {
		return &catalogv1alpha1.Comic{
			Spec:   catalogv1alpha1.ComicSpec{Folder: folder, SpecialVersion: v},
			Status: catalogv1alpha1.ComicStatus{Path: statusPath, Metadata: &catalogv1alpha1.ComicMetadata{Title: "Saga", Year: 2012}},
		}
	}
	issue := &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}}

	for _, tc := range []struct {
		name  string
		comic *catalogv1alpha1.Comic
		want  string
	}{
		{"a normal run", comic("", nil, catalogv1alpha1.ComicSpecialVersionNormal), "/data/comics/Saga/Saga c001.cbz"},
		{"no special version at all", comic("", nil, ""), "/data/comics/Saga/Saga c001.cbz"},
		{"volumes as issues name nothing more", comic("", nil, catalogv1alpha1.ComicSpecialVersionVolumeAsIssue), "/data/comics/Saga/Saga c001.cbz"},
		{"a trade paperback", comic("", nil, catalogv1alpha1.ComicSpecialVersionTPB), "/data/comics/Saga/Saga c001 TPB.cbz"},
		{"a one-shot", comic("", nil, catalogv1alpha1.ComicSpecialVersionOneShot), "/data/comics/Saga/Saga c001 One-Shot.cbz"},
		{"a hardcover", comic("", nil, catalogv1alpha1.ComicSpecialVersionHardCover), "/data/comics/Saga/Saga c001 Hard Cover.cbz"},
		{"an omnibus", comic("", nil, catalogv1alpha1.ComicSpecialVersionOmnibus), "/data/comics/Saga/Saga c001 Omnibus.cbz"},
		{"the comic's spec.folder", comic("", ptr.To("Saga (Image)"), ""), "/data/comics/Saga (Image)/Saga c001.cbz"},
		{"the comic's resolved path", comic("/data/comics/Saga (2012)", nil, ""), "/data/comics/Saga (2012)/Saga c001.cbz"},
	} {
		c, ok := catalogctx.Issue(tc.comic, issue)
		require.True(t, ok, tc.name)
		assert.Equal(t, 2012, c.Year, tc.name)
		got, err := catalogctx.IssueFilePath(root, tc.comic, c, ".cbz")
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}

	_, ok := catalogctx.Issue(&catalogv1alpha1.Comic{}, issue)
	assert.False(t, ok, "no comic metadata")
}

// TestFileExtByKind: a video file takes its probed container's extension,
// every other kind keeps its own, lowercased -- an .m4b probes as MP4.
func TestFileExtByKind(t *testing.T) {
	mp4 := &commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2"}
	assert.Equal(t, ".mp4", catalogctx.FileExt(commonv1.MediaKindMovie, mp4, "/x/film.mkv"))
	assert.Equal(t, ".mp4", catalogctx.FileExt(commonv1.MediaKindEpisode, mp4, "/x/ep.mkv"))
	assert.Equal(t, ".m4b", catalogctx.FileExt(commonv1.MediaKindAudiobook, mp4, "/x/Book.M4B"))
	assert.Equal(t, ".m4a", catalogctx.FileExt(commonv1.MediaKindAlbum, mp4, "/x/01 Track.m4a"))
	assert.Equal(t, ".flac", catalogctx.FileExt(commonv1.MediaKindAlbum, &commonv1.MediaInfo{Container: "flac"}, "/x/01.FLAC"))
	assert.Equal(t, ".epub", catalogctx.FileExt(commonv1.MediaKindBook, nil, "/x/Dune.epub"))
	assert.Equal(t, ".cbz", catalogctx.FileExt(commonv1.MediaKindIssue, &commonv1.MediaInfo{Container: "cbz", PageCount: 24}, "/x/Saga 001.CBZ"))
}

// TestReleaseYearReadsUTC: a date that round-tripped into a west-of-UTC
// local zone still reads the year the provider sent.
func TestReleaseYearReadsUTC(t *testing.T) {
	west := time.FixedZone("west", -8*3600)
	d := metav1.NewTime(time.Date(2020, time.January, 1, 0, 30, 0, 0, time.UTC).In(west))
	assert.Equal(t, 2020, catalogctx.ReleaseYear(&d))
	assert.Equal(t, 0, catalogctx.ReleaseYear(nil))
	assert.Equal(t, 0, catalogctx.ReleaseYear(&metav1.Time{}))
}

// TestSpecialVersionLabel pins Kapowarr's labels.
func TestSpecialVersionLabel(t *testing.T) {
	assert.Equal(t, "", catalogctx.SpecialVersionLabel(""))
	assert.Equal(t, "", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionNormal))
	assert.Equal(t, "", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionVolumeAsIssue))
	assert.Equal(t, "TPB", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionTPB))
	assert.Equal(t, "One-Shot", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionOneShot))
	assert.Equal(t, "Hard Cover", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionHardCover))
	assert.Equal(t, "Omnibus", catalogctx.SpecialVersionLabel(catalogv1alpha1.ComicSpecialVersionOmnibus))
}

// TestPartNumbers is the numbering {PartNumber} renders (2026-11-10 design
// D7, D11): an audiobook's parts in play order -- a two-disc set's tagged
// keys (disc*1000+track) come out 1..n, never as the keys themselves; the
// name's part numbers sort among them; an unnumbered part comes after every
// numbered one, ties by path -- and a lone part is numbered 0, so a one-file
// audiobook carries no part.
func TestPartNumbers(t *testing.T) {
	twoDiscs := []catalogctx.Part{
		{Index: 2001, Path: "/dl/CD2/Chapter 05.mp3"},
		{Index: 1001, Path: "/dl/CD1/Chapter 01.mp3"},
		{Index: 1003, Path: "/dl/CD1/Chapter 03.mp3"},
		{Index: 1002, Path: "/dl/CD1/Chapter 02.mp3"},
	}
	assert.Equal(t, []int32{4, 1, 3, 2}, catalogctx.PartNumbers(twoDiscs), "disc 2 track 1 is the fourth part, not part 2001")

	mixed := []catalogctx.Part{
		{Index: 0, Path: "/dl/zz unnumbered.mp3"},
		{Index: 7, Path: "/dl/Book - Part 07.mp3"},
		{Index: 0, Path: "/dl/aa unnumbered.mp3"},
		{Index: 1003, Path: "/dl/tagged.mp3"},
		{Index: 7, Path: "/dl/Book - Part 07 (copy).mp3"},
	}
	assert.Equal(t, []int32{5, 2, 4, 3, 1}, catalogctx.PartNumbers(mixed),
		"numbered parts first by index, a tie by path (a space sorts before a dot), the unnumbered after them by path")

	assert.Equal(t, []int32{0}, catalogctx.PartNumbers([]catalogctx.Part{{Index: 1003, Path: "/dl/only.m4b"}}), "a lone part has no number")
	assert.Empty(t, catalogctx.PartNumbers(nil))

	c, ok := catalogctx.Audiobook(&catalogv1alpha1.Audiobook{
		Status: catalogv1alpha1.AudiobookStatus{PartCount: 4, Metadata: &catalogv1alpha1.AudiobookMetadata{Title: "Guards! Guards!"}},
	}, 3)
	require.True(t, ok)
	assert.Equal(t, 3, c.PartNumber, "the number is what the caller numbered, never a partIndex")
	assert.Equal(t, 4, c.PartCount)
}
