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

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestSafeRemoveRefusesALexicalEscape(t *testing.T) {
	root := t.TempDir()
	err := fsops.SafeRemove(root, "../../etc/passwd")
	require.ErrorIs(t, err, fsops.ErrEscapesRoot)
}

func TestSafeRemoveRefusesASymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o664))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))

	err := fsops.SafeRemove(root, filepath.Join("escape", "secret.txt"))
	require.ErrorIs(t, err, fsops.ErrEscapesRoot)

	_, statErr := os.Stat(filepath.Join(outside, "secret.txt"))
	require.NoError(t, statErr, "the file outside root must survive")
}

func TestSafeRemoveRemovesAFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "gone.mkv"), []byte("x"), 0o664))

	require.NoError(t, fsops.SafeRemove(root, "gone.mkv"))

	_, err := os.Stat(filepath.Join(root, "gone.mkv"))
	require.True(t, os.IsNotExist(err))
}
