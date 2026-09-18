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

package subtitles_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

// clearUmask drops the process umask for the duration of a test so an
// assertion on the created file's mode sees the mode that was asked for.
// Production sets UMASK 002 (spec section 11) so a 0664 sidecar survives
// there; a developer's 022 would silently strip the group write bit.
func clearUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestWriterWritesAtomicallyAndOverwrites(t *testing.T) {
	clearUmask(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "Movie.en.srt")

	w := subtitles.NewWriter()
	require.NoError(t, w.Write(context.Background(), path, []byte("first"), 0o664))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))

	// No leftover temp file in the directory.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	// The sidecar is group-readable and group-writable: a media server
	// running as another uid in the same group has to be able to read it
	// (RootFolderSpec.Perms.FileMode defaults to "0664").
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o664), fi.Mode().Perm())

	// A pre-existing file at the destination is replaced, and keeps the
	// mode the caller asked for.
	require.NoError(t, w.Write(context.Background(), path, []byte("second, longer content"), 0o664))
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second, longer content", string(got))
	fi, err = os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o664), fi.Mode().Perm())
	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no .partial left behind")
}

func TestWriterHonoursARestrictiveMode(t *testing.T) {
	clearUmask(t)
	path := filepath.Join(t.TempDir(), "Movie.en.srt")
	require.NoError(t, subtitles.NewWriter().Write(context.Background(), path, []byte("x"), 0o600))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestWriterFailsCleanlyOnAMissingDirectory(t *testing.T) {
	w := subtitles.NewWriter()
	err := w.Write(context.Background(), filepath.Join(t.TempDir(), "no-such-dir", "Movie.en.srt"), []byte("x"), 0o664)
	assert.Error(t, err)
}
