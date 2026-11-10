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
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// folderTitles are the titles the folder rule and the file path must agree
// on (2026-11-10 design §5.5, task F1): a plain one, a trailing dot behind
// a question mark (TMDB's "What If...?"), every character the sanitizer
// rewrites or drops, a trailing dot alone, and 390 bytes, over the 255-byte
// component limit.
var folderTitles = []string{
	"Plain Title",
	"What If...?",
	`A:B\C/D>E<F?G*H|I"J`,
	"Trailing dot.",
	strings.Repeat("Long ", 78),
}

// TestItemFolderIsTheFolderOfTheFilePath: for every kind with a folder of
// its own, the folder ItemFolder (BookFolder, AlbumFolder) renders from an
// object is the directory the kind's *FilePath places the object's file in,
// byte for byte, over every title shape -- the one rule the controllers
// write status.path by and the import places by.
func TestItemFolderIsTheFolderOfTheFilePath(t *testing.T) {
	for _, title := range folderTitles {
		t.Run(title, func(t *testing.T) {
			root := rootFolder("/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
			m := movieTitled(title)
			c, ok := catalogctx.Movie(m)
			require.True(t, ok)
			file, err := catalogctx.MovieFilePath(root, m, c, ".mkv")
			require.NoError(t, err)
			folder, err := catalogctx.ItemFolder(root, commonv1.MediaKindMovie, m.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), folder, "movie")

			root = rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries)
			s := seriesTitled(title)
			eps := []catalogv1alpha1.Episode{{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{Title: "Pilot"}}}
			c, ok = catalogctx.Episode(s, eps)
			require.True(t, ok)
			file, err = catalogctx.EpisodeFilePath(root, s, c, ".mkv")
			require.NoError(t, err)
			folder, err = catalogctx.ItemFolder(root, commonv1.MediaKindSeries, s.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(filepath.Dir(file)), folder, "series: the season folder lies between")

			root = rootFolder("/data/media/books", catalogv1alpha1.RootFolderKindBook)
			author := &catalogv1alpha1.Author{Status: catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: title}}}
			book := bookTitled("A Book")
			c, ok = catalogctx.Book(author, book)
			require.True(t, ok)
			file, err = catalogctx.BookFilePath(root, author, c, ".epub")
			require.NoError(t, err)
			folder, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, author.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(filepath.Dir(file)), folder, "author: the book's own segment lies between")
			bookFolder, err := catalogctx.BookFolder(root, author, c)
			require.NoError(t, err)
			assert.Equal(t, folder, bookFolder, "an author's book's folder is its author's")

			book = bookTitled(title)
			c, ok = catalogctx.Book(nil, book)
			require.True(t, ok)
			file, err = catalogctx.BookFilePath(root, nil, c, ".epub")
			require.NoError(t, err)
			folder, err = catalogctx.BookFolder(root, nil, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), folder, "standalone book")

			root = rootFolder("/data/media/audiobooks", catalogv1alpha1.RootFolderKindAudiobook)
			ab := audiobookTitled(title)
			c, ok = catalogctx.Audiobook(ab, 0)
			require.True(t, ok)
			file, err = catalogctx.AudiobookFilePath(root, ab, c, ".m4b")
			require.NoError(t, err)
			folder, err = catalogctx.ItemFolder(root, commonv1.MediaKindAudiobook, ab.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), folder, "audiobook")

			root = rootFolder("/data/media/comics", catalogv1alpha1.RootFolderKindComic)
			comic := comicTitled(title)
			issue := &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "1"}}
			c, ok = catalogctx.Issue(comic, issue)
			require.True(t, ok)
			file, err = catalogctx.IssueFilePath(root, comic, c, ".cbz")
			require.NoError(t, err)
			folder, err = catalogctx.ItemFolder(root, commonv1.MediaKindComic, comic.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), folder, "comic")

			root = rootFolder("/data/media/music", catalogv1alpha1.RootFolderKindMusic)
			artist, album, track := artistAlbum(title, "An Album")
			c, ok = catalogctx.Album(artist, album, track)
			require.True(t, ok)
			file, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
			require.NoError(t, err)
			folder, err = catalogctx.ItemFolder(root, commonv1.MediaKindArtist, artist.Spec.Folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(filepath.Dir(file)), folder, "artist: the album folder lies between")
			albumFolder, err := catalogctx.AlbumFolder(root, folder, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), albumFolder, "album")

			artist, album, track = artistAlbum("An Artist", title)
			c, ok = catalogctx.Album(artist, album, track)
			require.True(t, ok)
			file, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
			require.NoError(t, err)
			albumFolder, err = catalogctx.AlbumFolder(root, "/data/media/music/An Artist", c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), albumFolder, "album titled %q", title)
		})
	}
}

// TestItemFolderIsSanitizedAsTheFileIs pins the shapes: the folder is the
// sanitized title, never the title as written, so an item's status.path
// names the folder on disk (until 2026-11-10 the controllers joined the
// title as written while the import placed the file under the sanitized
// one).
func TestItemFolderIsSanitizedAsTheFileIs(t *testing.T) {
	root := rootFolder("/data/media/comics", catalogv1alpha1.RootFolderKindComic)
	for title, want := range map[string]string{
		"What If...?":            "/data/media/comics/What If",
		`A:B\C/D>E<F?G*H|I"J`:    "/data/media/comics/A-B+C+DEFGH-I'J",
		"Trailing dot.":          "/data/media/comics/Trailing dot",
		strings.Repeat("x", 300): "/data/media/comics/" + strings.Repeat("x", 255),
	} {
		got, err := catalogctx.ItemFolder(root, commonv1.MediaKindComic, nil, naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: title})
		require.NoError(t, err, title)
		assert.Equal(t, want, got, title)
	}
	got, err := catalogctx.ItemFolder(root, commonv1.MediaKindComic, new("Marvel/What If...?"), naming.Context{})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/Marvel/What If", got, "a spec.folder's components are sanitized too, a literal '/' separating them")
}

// TestAStaleStatusPathRendersTheSameFilePath is the proof that a changed
// status.path moves no file on a deployed cluster: a container's status.path
// written by the old rule (the title as written, path.Join) and by the new
// one (the sanitized folder) render the same file path, byte for byte, as
// does no status.path at all -- joinUnder sanitized the whole path all
// along, so the naming planner's proposal, and NamingCurrent with it, is
// what it was, and the rename actuator has nothing to move.
func TestAStaleStatusPathRendersTheSameFilePath(t *testing.T) {
	const title = "What If...?"
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})

	root := rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries)
	s := seriesTitled(title)
	eps := []catalogv1alpha1.Episode{{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{Title: "Pilot"}}}
	c, ok := catalogctx.Episode(s, eps)
	require.True(t, ok)
	fresh, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, "/data/media/series/What If... (2010) [tvdbid-266189]/Season 01/What If... (2010) - S01E01 - Pilot.mkv", fresh)
	oldRendered, err := eng.SeriesFolder(c)
	require.NoError(t, err)
	s.Status.Path = path.Join(root.Spec.Path, oldRendered)
	assert.Equal(t, "/data/media/series/What If...? (2010) [tvdbid-266189]", s.Status.Path, "the old rule's status.path, the title as written")
	stale, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "a series' stale status.path renders the same file path")
	s.Status.Path, err = catalogctx.ItemFolder(root, commonv1.MediaKindSeries, nil, c)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/series/What If... (2010) [tvdbid-266189]", s.Status.Path, "the new rule's status.path, the folder on disk")
	current, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, fresh, current, "a series' new status.path renders the same file path")

	root = rootFolder("/data/media/comics", catalogv1alpha1.RootFolderKindComic)
	comic := comicTitled(title)
	issue := &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "1"}}
	c, ok = catalogctx.Issue(comic, issue)
	require.True(t, ok)
	fresh, err = catalogctx.IssueFilePath(root, comic, c, ".cbz")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(fresh, "/data/media/comics/What If/"), fresh)
	comic.Status.Path = oldFolder(t, eng, commonv1.MediaKindComic, root, c)
	assert.Equal(t, "/data/media/comics/What If...?", comic.Status.Path)
	stale, err = catalogctx.IssueFilePath(root, comic, c, ".cbz")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "a comic's stale status.path renders the same file path")

	root = rootFolder("/data/media/audiobooks", catalogv1alpha1.RootFolderKindAudiobook)
	ab := audiobookTitled(title)
	c, ok = catalogctx.Audiobook(ab, 0)
	require.True(t, ok)
	fresh, err = catalogctx.AudiobookFilePath(root, ab, c, ".m4b")
	require.NoError(t, err)
	ab.Status.Path = oldFolder(t, eng, commonv1.MediaKindAudiobook, root, c)
	assert.Equal(t, "/data/media/audiobooks/An Author/What If...? A Narrator", ab.Status.Path)
	stale, err = catalogctx.AudiobookFilePath(root, ab, c, ".m4b")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "an audiobook's stale status.path renders the same file path")

	root = rootFolder("/data/media/books", catalogv1alpha1.RootFolderKindBook)
	author := &catalogv1alpha1.Author{Status: catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: title}}}
	c, ok = catalogctx.Book(author, bookTitled("A Book"))
	require.True(t, ok)
	fresh, err = catalogctx.BookFilePath(root, author, c, ".epub")
	require.NoError(t, err)
	author.Status.Path = oldFolder(t, eng, commonv1.MediaKindAuthor, root, c)
	assert.Equal(t, "/data/media/books/What If...?", author.Status.Path)
	stale, err = catalogctx.BookFilePath(root, author, c, ".epub")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "an author's stale status.path renders the same file path")

	root = rootFolder("/data/media/music", catalogv1alpha1.RootFolderKindMusic)
	artist, album, track := artistAlbum(title, "An Album")
	c, ok = catalogctx.Album(artist, album, track)
	require.True(t, ok)
	fresh, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
	require.NoError(t, err)
	artist.Status.Path = oldFolder(t, eng, commonv1.MediaKindArtist, root, c)
	assert.Equal(t, "/data/media/music/What If...?", artist.Status.Path)
	stale, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "an artist's stale status.path renders the same file path")
	album.Status.Path = path.Join(artist.Status.Path, path.Base(oldFolder(t, eng, commonv1.MediaKindAlbum, root, c)))
	assert.Equal(t, "/data/media/music/What If...?/An Album", album.Status.Path)
	stale, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
	require.NoError(t, err)
	assert.Equal(t, fresh, stale, "an album's stale status.path renders the same file path")
}

// TestItemFolderDegrades: a container whose name has not arrived lives at
// its root; a known title the engine refuses or the sanitizer empties is an
// error (the controllers' ReasonFolderUnrenderable), as is a spec.folder
// that would leave the root; a kind with no folder of its own under the
// root is naming.ErrNoFolder.
func TestItemFolderDegrades(t *testing.T) {
	root := rootFolder("/data/media/books", catalogv1alpha1.RootFolderKindBook)

	got, err := catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, nil, naming.Context{Kind: commonv1.MediaKindAuthor})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books", got, "an author without a name yet lives at the root")
	got, err = catalogctx.BookFolder(root, &catalogv1alpha1.Author{}, naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "A Book"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books", got, "an author's book reads the root while the author's name is unknown")
	got, err = catalogctx.BookFolder(root, nil, naming.Context{Kind: commonv1.MediaKindBook})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books", got, "a standalone book without a title yet lives at the root")

	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, nil, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "???"})
	require.Error(t, err, "a name that sanitizes to nothing")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, nil, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "..."})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a name of only dots")
	_, err = catalogctx.BookFolder(root, nil, naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "???"})
	require.Error(t, err, "a standalone book's title that sanitizes to nothing")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new(".."), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder naming the root's parent")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new("x/../../etc"), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.Error(t, err, "a spec.folder that would leave the root")

	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindEpisode, commonv1.MediaKindIssue} {
		_, err = catalogctx.ItemFolder(root, kind, nil, naming.Context{})
		require.ErrorIs(t, err, naming.ErrNoFolder, "%s has no folder of its own under the root", kind)
	}

	music := rootFolder("/data/media/music", catalogv1alpha1.RootFolderKindMusic)
	got, err = catalogctx.AlbumFolder(music, "/data/media/music/An Artist", naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "An Artist"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/An Artist", got, "an album without a title yet lives in its artist's folder")
	got, err = catalogctx.AlbumFolder(music, "/data/media/music/An Artist", naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "Weezer", AlbumTitle: "Weezer"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/An Artist/Weezer", got, "an album titled as its artist is still its own folder")
	_, err = catalogctx.AlbumFolder(music, "/data/media/music/An Artist", naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "An Artist", AlbumTitle: "???"})
	require.Error(t, err, "an album title that sanitizes to nothing")
}

// oldFolder is the rule every controller wrote status.path by until
// 2026-11-10: the kind's preset joined as rendered, no component
// sanitized (path.Join).
func oldFolder(t *testing.T, eng naming.Engine, kind commonv1.MediaKind, root *catalogv1alpha1.RootFolder, c naming.Context) string {
	t.Helper()
	rendered, err := eng.BuildFolder(kind, c)
	require.NoError(t, err)
	return path.Join(root.Spec.Path, rendered)
}

func bookTitled(title string) *catalogv1alpha1.Book {
	released := metav1.NewTime(metav1.Now().AddDate(-1, 0, 0))
	return &catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: title, ReleaseDate: &released}}}
}

func audiobookTitled(title string) *catalogv1alpha1.Audiobook {
	return &catalogv1alpha1.Audiobook{Status: catalogv1alpha1.AudiobookStatus{Metadata: &catalogv1alpha1.AudiobookMetadata{
		Title: title, Authors: []catalogv1alpha1.NamedRef{{Name: "An Author"}}, Narrators: []string{"A Narrator"},
	}}}
}

func comicTitled(title string) *catalogv1alpha1.Comic {
	return &catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{Title: title, Year: 2015}}}
}

func artistAlbum(artistName, albumTitle string) (*catalogv1alpha1.Artist, *catalogv1alpha1.Album, *catalogv1alpha1.Track) {
	artist := &catalogv1alpha1.Artist{
		Spec:   catalogv1alpha1.ArtistSpec{MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711"},
		Status: catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: artistName}},
	}
	album := &catalogv1alpha1.Album{
		Spec:   catalogv1alpha1.AlbumSpec{ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
		Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: albumTitle}},
	}
	track := &catalogv1alpha1.Track{Number: 1, Medium: 1, Title: "A Track", RecordingID: "r1"}
	return artist, album, track
}
