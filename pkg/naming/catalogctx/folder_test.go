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
			albumFolder, err := catalogctx.AlbumFolder(root, artist, c)
			require.NoError(t, err)
			assert.Equal(t, filepath.Dir(file), albumFolder, "album")

			artist, album, track = artistAlbum("An Artist", title)
			c, ok = catalogctx.Album(artist, album, track)
			require.True(t, ok)
			file, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
			require.NoError(t, err)
			albumFolder, err = catalogctx.AlbumFolder(root, artist, c)
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
	assert.Equal(t, "/data/media/comics/Marvel/What If...?", got, "a pinned spec.folder is joined as written, a '/' separating its components")
}

// TestAPinnedFolderIsTakenAsWritten: an item's spec.folder names a folder
// on disk, not a title, so it is joined as written on both sides -- the
// scan applier pins a scanned Series to the folder whose TheTVDB id named
// it (scanapply.seriesFolder), "What If...? (2021) [tvdbid-266189]" with
// its "?" and its dot -- and the folder the controller writes is the one
// the import places the files in, whether or not status.path is written
// yet. Until the F1 fix round (2026-11-10) both sides sanitized the pin,
// so status.path named "What If... (2021) [tvdbid-266189]", which was not
// on disk, and a new episode landed in that folder beside the scanned one.
func TestAPinnedFolderIsTakenAsWritten(t *testing.T) {
	const pin = "What If...? (2021) [tvdbid-266189]"
	root := rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries)
	s := seriesTitled("What If...?")
	s.Spec.Folder = new(pin)
	eps := []catalogv1alpha1.Episode{{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{Title: "Pilot"}}}
	c, ok := catalogctx.Episode(s, eps)
	require.True(t, ok)
	folder, err := catalogctx.ItemFolder(root, commonv1.MediaKindSeries, s.Spec.Folder, c)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/series/"+pin, folder, "the scanned folder, as the scan applier pinned it")
	file, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, "/data/media/series/"+pin+"/Season 01/What If... (2010) - S01E01 - Pilot.mkv", file,
		"the episode lands in the scanned folder; its own name is still sanitized")
	s.Status.Path = folder
	again, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, file, again, "the written status.path places the episode the same")
	s.Status.Path = "/data/media/series/What If... (2021) [tvdbid-266189]"
	again, err = catalogctx.EpisodeFilePath(root, s, c, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, file, again, "a status.path the sanitizing rule wrote for a pinned series is not read: the pin is the folder")

	// Every kind with a pin, over a pin of a "?", a trailing dot and a
	// subfolder: the controller's folder and the import's file agree, and
	// the pin's components are the folder's, byte for byte.
	const deep = "Pinned?/Sub."
	movies := rootFolder("/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	m := movieTitled("Heat")
	m.Spec.Folder = new(deep)
	mc, ok := catalogctx.Movie(m)
	require.True(t, ok)
	folder, err = catalogctx.ItemFolder(movies, commonv1.MediaKindMovie, m.Spec.Folder, mc)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Pinned?/Sub.", folder, "movie")
	file, err = catalogctx.MovieFilePath(movies, m, mc, ".mkv")
	require.NoError(t, err)
	assert.Equal(t, folder, filepath.Dir(file), "movie")

	books := rootFolder("/data/media/books", catalogv1alpha1.RootFolderKindBook)
	author := &catalogv1alpha1.Author{Spec: catalogv1alpha1.AuthorSpec{Folder: new(deep)}, Status: catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "An Author"}}}
	bc, ok := catalogctx.Book(author, bookTitled("A Book"))
	require.True(t, ok)
	folder, err = catalogctx.ItemFolder(books, commonv1.MediaKindAuthor, author.Spec.Folder, bc)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books/Pinned?/Sub.", folder, "author")
	bookFolder, err := catalogctx.BookFolder(books, author, bc)
	require.NoError(t, err)
	assert.Equal(t, folder, bookFolder, "an author's book's folder is its author's pin")
	file, err = catalogctx.BookFilePath(books, author, bc, ".epub")
	require.NoError(t, err)
	assert.Equal(t, folder, filepath.Dir(filepath.Dir(file)), "author: the book's own segment lies between")
	author.Status.Path = "/data/media/books/Pinned/Sub"
	again, err = catalogctx.BookFilePath(books, author, bc, ".epub")
	require.NoError(t, err)
	assert.Equal(t, file, again, "a pinned author's stale sanitized status.path is not read")

	audiobooks := rootFolder("/data/media/audiobooks", catalogv1alpha1.RootFolderKindAudiobook)
	ab := audiobookTitled("A Title")
	ab.Spec.Folder = new(deep)
	ac, ok := catalogctx.Audiobook(ab, 0)
	require.True(t, ok)
	folder, err = catalogctx.ItemFolder(audiobooks, commonv1.MediaKindAudiobook, ab.Spec.Folder, ac)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Pinned?/Sub.", folder, "audiobook")
	file, err = catalogctx.AudiobookFilePath(audiobooks, ab, ac, ".m4b")
	require.NoError(t, err)
	assert.Equal(t, folder, filepath.Dir(file), "audiobook")

	comics := rootFolder("/data/media/comics", catalogv1alpha1.RootFolderKindComic)
	comic := comicTitled("A Comic")
	comic.Spec.Folder = new(deep)
	ic, ok := catalogctx.Issue(comic, &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "1"}})
	require.True(t, ok)
	folder, err = catalogctx.ItemFolder(comics, commonv1.MediaKindComic, comic.Spec.Folder, ic)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/Pinned?/Sub.", folder, "comic")
	file, err = catalogctx.IssueFilePath(comics, comic, ic, ".cbz")
	require.NoError(t, err)
	assert.Equal(t, folder, filepath.Dir(file), "comic")

	music := rootFolder("/data/media/music", catalogv1alpha1.RootFolderKindMusic)
	artist, album, track := artistAlbum("An Artist", "An Album")
	artist.Spec.Folder = new(deep)
	tc, ok := catalogctx.Album(artist, album, track)
	require.True(t, ok)
	folder, err = catalogctx.ItemFolder(music, commonv1.MediaKindArtist, artist.Spec.Folder, tc)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/Pinned?/Sub.", folder, "artist")
	albumFolder, err := catalogctx.AlbumFolder(music, artist, tc)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/Pinned?/Sub./An Album", albumFolder, "the album segment is rendered and sanitized under the pin as written")
	file, err = catalogctx.TrackFilePath(music, artist, album, tc, ".flac")
	require.NoError(t, err)
	assert.Equal(t, albumFolder, filepath.Dir(file), "album under a pinned artist")
	album.Status.Path = "/data/media/music/Pinned/Sub/An Album"
	again, err = catalogctx.TrackFilePath(music, artist, album, tc, ".flac")
	require.NoError(t, err)
	assert.Equal(t, file, again, "an album's stale sanitized status.path under a pinned artist is not read")
}

// TestAStaleStatusPathRendersTheSameFilePath is the proof that a changed
// status.path moves no file on a deployed cluster: for every title shape
// (folderTitles, the 390-byte one included), a container's status.path
// written by the old rule (the title as written, path.Join) and by the new
// one (the sanitized folder) render the same file path, byte for byte, as
// does no status.path at all -- a rendered status.path is sanitized below
// the root to the sanitizer's fixed point (renderedBase, sanitizeFixed) as
// the whole path was at every join until then -- so the naming planner's
// proposal, and NamingCurrent with it, is what it was, and the rename
// actuator has nothing to move. The F1 review (2026-11-10) found the
// 390-byte title failing this under one sanitizer pass: the old rule's raw
// component cut at 255 bytes kept a trailing space that a second pass
// trimmed, so the stale path and the fresh one named folders a byte apart.
func TestAStaleStatusPathRendersTheSameFilePath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	for _, title := range folderTitles {
		t.Run(title, func(t *testing.T) {
			whatIf := title == "What If...?"

			root := rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries)
			s := seriesTitled(title)
			eps := []catalogv1alpha1.Episode{{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{Title: "Pilot"}}}
			c, ok := catalogctx.Episode(s, eps)
			require.True(t, ok)
			fresh, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
			require.NoError(t, err)
			if whatIf {
				assert.Equal(t, "/data/media/series/What If... (2010) [tvdbid-266189]/Season 01/What If... (2010) - S01E01 - Pilot.mkv", fresh)
			}
			oldRendered, err := eng.SeriesFolder(c)
			require.NoError(t, err)
			s.Status.Path = path.Join(root.Spec.Path, oldRendered)
			if whatIf {
				assert.Equal(t, "/data/media/series/What If...? (2010) [tvdbid-266189]", s.Status.Path, "the old rule's status.path, the title as written")
			}
			stale, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
			require.NoError(t, err)
			assert.Equal(t, fresh, stale, "a series' stale status.path renders the same file path")
			s.Status.Path, err = catalogctx.ItemFolder(root, commonv1.MediaKindSeries, nil, c)
			require.NoError(t, err)
			if whatIf {
				assert.Equal(t, "/data/media/series/What If... (2010) [tvdbid-266189]", s.Status.Path, "the new rule's status.path, the folder on disk")
			}
			assert.Equal(t, s.Status.Path, naming.SanitizePath(s.Status.Path, naming.DefaultSanitizeOptions()), "the new rule's status.path is a fixed point of the sanitizer")
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
			if whatIf {
				assert.True(t, strings.HasPrefix(fresh, "/data/media/comics/What If/"), fresh)
			}
			comic.Status.Path = oldFolder(t, eng, commonv1.MediaKindComic, root, c)
			if whatIf {
				assert.Equal(t, "/data/media/comics/What If...?", comic.Status.Path)
			}
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
			if whatIf {
				assert.Equal(t, "/data/media/audiobooks/An Author/What If...? A Narrator", ab.Status.Path)
			}
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
			if whatIf {
				assert.Equal(t, "/data/media/books/What If...?", author.Status.Path)
			}
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
			if whatIf {
				assert.Equal(t, "/data/media/music/What If...?", artist.Status.Path)
			}
			stale, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
			require.NoError(t, err)
			assert.Equal(t, fresh, stale, "an artist's stale status.path renders the same file path")
			album.Status.Path = path.Join(artist.Status.Path, path.Base(oldFolder(t, eng, commonv1.MediaKindAlbum, root, c)))
			if whatIf {
				assert.Equal(t, "/data/media/music/What If...?/An Album", album.Status.Path)
			}
			stale, err = catalogctx.TrackFilePath(root, artist, album, c, ".flac")
			require.NoError(t, err)
			assert.Equal(t, fresh, stale, "an album's stale status.path renders the same file path")
		})
	}
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
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder that would leave the root walks the tree")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new("a/./b"), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder component of '.'")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new("a/ /b"), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a blank spec.folder component")
	_, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new("/"), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.Error(t, err, "a spec.folder of separators alone names the root itself")
	got, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new("   "), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books/Author", got, "a blank spec.folder pins nothing: the preset renders, never the root itself")
	got, err = catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, new(" Pinned "), naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "Author"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/books/Pinned", got, "a pin is read without the whitespace around it")

	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindEpisode, commonv1.MediaKindIssue} {
		_, err = catalogctx.ItemFolder(root, kind, nil, naming.Context{})
		require.ErrorIs(t, err, naming.ErrNoFolder, "%s has no folder of its own under the root", kind)
	}

	music := rootFolder("/data/media/music", catalogv1alpha1.RootFolderKindMusic)
	anArtist := &catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{Path: "/data/media/music/An Artist"}}
	got, err = catalogctx.AlbumFolder(music, anArtist, naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "An Artist"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/An Artist", got, "an album without a title yet lives in its artist's folder")
	got, err = catalogctx.AlbumFolder(music, anArtist, naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "Weezer", AlbumTitle: "Weezer"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music/An Artist/Weezer", got, "an album titled as its artist is still its own folder")
	_, err = catalogctx.AlbumFolder(music, anArtist, naming.Context{Kind: commonv1.MediaKindAlbum, ArtistName: "An Artist", AlbumTitle: "???"})
	require.Error(t, err, "an album title that sanitizes to nothing")
	unnamed := &catalogv1alpha1.Artist{}
	got, err = catalogctx.AlbumFolder(music, unnamed, naming.Context{Kind: commonv1.MediaKindAlbum})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/music", got, "an album of an artist whose name has not arrived lives at the root, as the artist does")
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
