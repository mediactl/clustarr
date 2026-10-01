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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncFileFsyncsAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.part-0123abcd-1.mkv")
	require.NoError(t, os.WriteFile(path, []byte("payload"), 0o664))

	var synced string
	old := fileSyncFunc
	t.Cleanup(func() { fileSyncFunc = old })
	fileSyncFunc = func(f *os.File) error {
		synced = f.Name()
		return old(f)
	}

	require.NoError(t, SyncFile(path))
	assert.Equal(t, path, synced, "the file itself is fsynced, not only its directory")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got), "a sync changes nothing")
}

// A delayed write error -- the one a close(2) or fsync(2) reports after
// write(2) returned success -- reaches the caller, naming the file.
func TestSyncFileReportsTheFsyncError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mkv")
	require.NoError(t, os.WriteFile(path, []byte("payload"), 0o664))

	eio := errors.New("input/output error")
	old := fileSyncFunc
	t.Cleanup(func() { fileSyncFunc = old })
	fileSyncFunc = func(*os.File) error { return eio }

	err := SyncFile(path)
	require.ErrorIs(t, err, eio)
	assert.Contains(t, err.Error(), path)
}

func TestSyncFileReportsAMissingFile(t *testing.T) {
	err := SyncFile(filepath.Join(t.TempDir(), "gone.mkv"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
