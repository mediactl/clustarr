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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetPermissionsChmodsRecursively(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(dir, 0o664, 0o775, -1, -1))

	fi, err := os.Stat(filepath.Join(dir, "sub"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o775), fi.Mode().Perm())

	fi, err = os.Stat(filepath.Join(dir, "sub", "file.mkv"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), fi.Mode().Perm())
}

func TestSetPermissionsSkipsChownWhenNotRoot(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 1000 }

	var chownCalls int
	oldChown := chownFunc
	t.Cleanup(func() { chownFunc = oldChown })
	chownFunc = func(string, int, int) error { chownCalls++; return nil }

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(dir, 0o664, 0o775, 1000, 100))
	require.Zero(t, chownCalls, "chown must be a no-op when not root")
}

func TestSetPermissionsChownsAsRoot(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }

	type call struct {
		path     string
		uid, gid int
	}
	var calls []call
	oldChown := chownFunc
	t.Cleanup(func() { chownFunc = oldChown })
	chownFunc = func(p string, uid, gid int) error {
		calls = append(calls, call{p, uid, gid})
		return nil
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(dir, 0o664, 0o775, -1, 100))
	require.Len(t, calls, 2, "root dir + file.mkv")
	for _, c := range calls {
		require.Equal(t, -1, c.uid)
		require.Equal(t, 100, c.gid)
	}
}
