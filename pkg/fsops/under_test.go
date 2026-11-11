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
package fsops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestStrictlyUnder(t *testing.T) {
	assert.True(t, fsops.StrictlyUnder("/data/media/tv/Andor", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("/data/media/tv", "/data/media/tv"), "the root itself")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv2/x", "/data/media/tv"), "a sibling with a shared prefix")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv/../movies/x", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("tv/Andor", "/data/media"), "relative")
}

func TestPruneEmptyDirsStopsBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "keep.txt"), []byte("x"), 0o644))
	fsops.PruneEmptyDirs(root, deep)
	assert.NoDirExists(t, filepath.Join(root, "a", "b"))
	assert.DirExists(t, filepath.Join(root, "a"), "not empty")
	assert.DirExists(t, root)
}

// MkdirUnder creates the missing folders strictly under its root and names
// the topmost it made, so PruneMade removes exactly those: an empty folder
// that was already there before the call -- a person's, or a scan's -- is
// never removed with them.
func TestMkdirUnderNamesWhatItMadeAndPruneMadeRemovesOnlyThat(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Nirvana")
	require.NoError(t, os.MkdirAll(filepath.Join(artist, "empty-before"), 0o755))
	album := filepath.Join(artist, "Nevermind (1991)", "CD 01")

	made, err := fsops.MkdirUnder(artist, album, 0o775)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(artist, "Nevermind (1991)"), made, "the topmost folder it created")
	assert.DirExists(t, album)

	again, err := fsops.MkdirUnder(artist, album, 0o775)
	require.NoError(t, err)
	assert.Empty(t, again, "a folder already there is not reported as made")

	fsops.PruneMade(made, album)
	assert.NoDirExists(t, filepath.Join(artist, "Nevermind (1991)"))
	assert.DirExists(t, filepath.Join(artist, "empty-before"), "an empty folder it did not make stays")
	assert.DirExists(t, artist, "the root is never removed")

	fsops.PruneMade("", album) // nothing made: a no-op
	assert.DirExists(t, artist)
}

// A folder outside the root, or a path through something that is not a
// directory -- a file, or a symbolic link that could lead a move out of
// the root -- is refused with nothing created.
func TestMkdirUnderRefusesWhatItMayNotCreate(t *testing.T) {
	root := t.TempDir()
	artist := filepath.Join(root, "Nirvana")
	require.NoError(t, os.MkdirAll(artist, 0o755))
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(artist, "link")))
	require.NoError(t, os.WriteFile(filepath.Join(artist, "Nevermind (1991)"), []byte("a file"), 0o644))

	_, err := fsops.MkdirUnder(artist, filepath.Join(root, "Other", "Album"), 0o775)
	require.ErrorIs(t, err, fsops.ErrNotUnder)
	assert.NoDirExists(t, filepath.Join(root, "Other"))

	_, err = fsops.MkdirUnder(artist, filepath.Join(artist, "link", "Album"), 0o775)
	require.ErrorIs(t, err, fsops.ErrNotDirectory, "through a symbolic link")
	assert.NoDirExists(t, filepath.Join(elsewhere, "Album"))

	_, err = fsops.MkdirUnder(artist, filepath.Join(artist, "Nevermind (1991)", "CD 01"), 0o775)
	require.ErrorIs(t, err, fsops.ErrNotDirectory, "through a file")
}

// The root itself is the folder an audiobook part, an issue or a
// standalone book is placed in (the 2026-11-10 non-video design's D19
// with every container whose preset names no subfolder): nothing is made,
// and nothing is reported as made.
func TestMkdirUnderCreatesNothingForTheRootItself(t *testing.T) {
	audiobook := filepath.Join(t.TempDir(), "Terry Pratchett", "Guards! Guards!")
	require.NoError(t, os.MkdirAll(audiobook, 0o755))
	for _, dir := range []string{audiobook, audiobook + string(filepath.Separator)} {
		made, err := fsops.MkdirUnder(audiobook, dir, 0o775)
		require.NoError(t, err, dir)
		assert.Empty(t, made, dir)
	}
	assert.DirExists(t, audiobook)
}

// A symbolic link anywhere between the root and the folder asked for is
// refused, live or dangling, as the folder itself or above it -- not only
// the deepest component that exists: an album folder linked elsewhere
// whose target holds the disc folder would otherwise be taken as found and
// a track moved through it out of the root.
func TestMkdirUnderRefusesASymbolicLinkAnywhereOnTheWay(t *testing.T) {
	artist := filepath.Join(t.TempDir(), "Nirvana")
	require.NoError(t, os.MkdirAll(artist, 0o755))
	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(elsewhere, "CD 01"), 0o755))
	album := filepath.Join(artist, "Nevermind (1991)")
	require.NoError(t, os.Symlink(elsewhere, album))
	gone := filepath.Join(t.TempDir(), "gone")
	dangling := filepath.Join(artist, "Bleach (1989)")
	require.NoError(t, os.Symlink(gone, dangling))

	for _, dir := range []string{
		filepath.Join(album, "CD 01"), // exists through the link
		filepath.Join(album, "CD 02"), // missing through the link
		album,                         // the link itself
		dangling,
		filepath.Join(dangling, "CD 01"),
	} {
		made, err := fsops.MkdirUnder(artist, dir, 0o775)
		require.ErrorIs(t, err, fsops.ErrNotDirectory, dir)
		assert.Empty(t, made, dir)
	}
	assert.NoDirExists(t, filepath.Join(elsewhere, "CD 02"), "nothing is made through the link")
	assert.NoFileExists(t, gone)
	assert.NoDirExists(t, gone, "nor at a dangling link's target")
}

// PruneEmptyDirs removes empty directories only: a symbolic link on the
// way -- which os.Remove would unlink whatever its target holds -- stops
// it with nothing removed, the link and everything behind it kept.
func TestPruneEmptyDirsNeverRemovesThroughASymbolicLink(t *testing.T) {
	artist := filepath.Join(t.TempDir(), "Nirvana")
	require.NoError(t, os.MkdirAll(artist, 0o755))
	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(elsewhere, "CD 01"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "02 In Bloom.flac"), []byte("x"), 0o644))
	link := filepath.Join(artist, "Nevermind (1991)")
	require.NoError(t, os.Symlink(elsewhere, link))

	fsops.PruneEmptyDirs(artist, link)
	_, err := os.Lstat(link)
	require.NoError(t, err, "the link to a folder that is not empty is kept")

	fsops.PruneEmptyDirs(artist, filepath.Join(link, "CD 01"))
	assert.DirExists(t, filepath.Join(elsewhere, "CD 01"), "an empty folder behind the link is outside the root")
	_, err = os.Lstat(link)
	require.NoError(t, err)
}
