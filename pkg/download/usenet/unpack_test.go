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

package usenet

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeJoinRefusesAnEntryThatEscapesTheDestination(t *testing.T) {
	// Zip slip. Archives come from an indexer, so entry names are
	// attacker-controlled.
	root := t.TempDir()
	for _, bad := range []string{
		"",
		"..",
		"../escape.sh",
		"../../etc/cron.d/x",
		`..\..\windows.bat`,
		"/etc/passwd",
		"/etc/passwd/../../../escape",
	} {
		_, err := safeJoin(root, bad)
		require.ErrorIsf(t, err, ErrUnsafeArchivePath, "entry %q must be refused, not laundered", bad)
	}

	ok, err := safeJoin(root, "sub/dir/movie.mkv")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "sub/dir/movie.mkv"), ok)

	ok, err = safeJoin(root, "./movie.mkv")
	require.NoError(t, err, "a benign leading ./ is not an escape")
	require.Equal(t, filepath.Join(root, "movie.mkv"), ok)
}

func TestUnpackZipRefusesAnEntryThatEscapes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.zip")

	f, err := os.Create(src)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w, err := zw.Create("../escaped.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("nope"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	dst := filepath.Join(dir, "out")
	_, err = unpackZip(context.Background(), src, dst)
	require.ErrorIs(t, err, ErrUnsafeArchivePath)
	require.NoFileExists(t, filepath.Join(dir, "escaped.txt"))
}

func TestUnpackArchivesReportsAnEncryptedZip(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "locked.zip"), encryptedZip(t), 0o644))

	res, err := unpackArchives(context.Background(), dir, filepath.Join(dir, "out"), "",
		[]nzbFile{{Name: "locked.zip", Kind: kindArchive}})
	require.ErrorIs(t, err, ErrEncrypted)
	require.True(t, res.Encrypted)
}

func TestUnpackArchivesSkipsAVolumeThatNeverLanded(t *testing.T) {
	// par2 could not replace a missing file. Extraction must not blow up on
	// its absence; the health gate has already decided whether the download
	// is viable.
	dir := t.TempDir()
	res, err := unpackArchives(context.Background(), dir, filepath.Join(dir, "out"), "",
		[]nzbFile{{Name: "gone.rar", Kind: kindArchive}})
	require.NoError(t, err)
	require.Zero(t, res.Extracted)
}

func TestCleanupRemovesPar2AndJunkButKeepsTheContent(t *testing.T) {
	dir := t.TempDir()
	files := []nzbFile{
		{Name: "movie.mkv", Kind: kindContent},
		{Name: "release.par2", Kind: kindPar2Index},
		{Name: "release.vol000+01.par2", Kind: kindPar2Volume},
		{Name: "release.rar", Kind: kindArchive},
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.Name), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.nfo"), []byte("x"), 0o644))

	require.NoError(t, cleanupAfterUnpack(context.Background(), dir, files, false, true, []string{"*.nfo"}))

	require.FileExists(t, filepath.Join(dir, "movie.mkv"))
	require.FileExists(t, filepath.Join(dir, "release.rar"), "archives stay when nothing was unpacked")
	require.NoFileExists(t, filepath.Join(dir, "release.par2"))
	require.NoFileExists(t, filepath.Join(dir, "release.vol000+01.par2"))
	require.NoFileExists(t, filepath.Join(dir, "readme.nfo"))
}

func TestIsFirstRarVolume(t *testing.T) {
	// The regression this pins: a lazy `(.*?)\.rar$` alternation matches
	// "release.part02.rar" by letting the prefix swallow ".part02", which made
	// every volume of a set an entry point.
	require.True(t, isFirstRarVolume("release.part01.rar"))
	require.True(t, isFirstRarVolume("release.part001.rar"))
	require.True(t, isFirstRarVolume("release.rar"))
	require.False(t, isFirstRarVolume("release.part02.rar"))
	require.False(t, isFirstRarVolume("release.part10.rar"))
	require.False(t, isFirstRarVolume("release.r00"))
	require.False(t, isFirstRarVolume("release.mkv"))
}
