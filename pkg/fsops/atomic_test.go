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

package fsops

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAtomicWriteReplacesTheTargetWithoutATemporaryFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o664))

	err := AtomicWrite(path, strings.NewReader("fresh content"), 0o664)
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "fresh content", string(got))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no .partial file should remain")
	require.Equal(t, "movie.nfo", entries[0].Name())
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("simulated read failure")
	}
	n := copy(p, bytes.Repeat([]byte{'x'}, r.n))
	r.n -= n
	return n, nil
}

func TestAtomicWriteLeavesNoPartialFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")

	err := AtomicWrite(path, &failingReader{n: 4}, 0o664)
	require.Error(t, err)

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "target must not exist")
	_, statErr = os.Stat(path + ".partial")
	require.True(t, os.IsNotExist(statErr), ".partial must not exist")
}

// TestAtomicWriteAppliesTheModeOverAStalePartialFile: O_CREATE applies mode
// only when it creates the file, so a .partial left behind by a crashed
// write (or by a process with a different umask) used to donate its own
// permissions to the renamed result. Callers pass the mode that must end up
// on disk -- RootFolderSpec.Perms.FileMode, 0664 by default, which a media
// server running as another uid in the media group has to be able to read.
func TestAtomicWriteAppliesTheModeOverAStalePartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	require.NoError(t, os.WriteFile(path+".partial", []byte("crashed"), 0o600))
	require.NoError(t, os.Chmod(path+".partial", 0o600)) // defeat the umask on creation

	require.NoError(t, AtomicWrite(path, strings.NewReader("fresh content"), 0o664))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), fi.Mode().Perm())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "fresh content", string(got))
}

// TestAtomicWriteModeSurvivesTheUmask: the mode a caller passes is the mode
// on disk, not the mode minus the ambient umask. This test deliberately does
// not clear the umask (this machine's is 022, which would strip the group
// write bit from 0664 at creation time).
func TestAtomicWriteModeSurvivesTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), "movie.nfo")
	require.NoError(t, AtomicWrite(path, strings.NewReader("x"), 0o664))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), fi.Mode().Perm())
}
