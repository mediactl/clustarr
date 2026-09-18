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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestSafeRemoveRefusesALexicalEscape(t *testing.T) {
	root := t.TempDir()
	err := fsops.SafeRemove(context.Background(), root, "../../etc/passwd")
	require.ErrorIs(t, err, fsops.ErrOutsideRoot)
}

func TestSafeRemoveRefusesASymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o664))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))

	err := fsops.SafeRemove(context.Background(), root, filepath.Join("escape", "secret.txt"))
	require.ErrorIs(t, err, fsops.ErrOutsideRoot)

	_, statErr := os.Stat(filepath.Join(outside, "secret.txt"))
	require.NoError(t, statErr, "the file outside root must survive")
}

func TestSafeRemoveRemovesAFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "gone.mkv"), []byte("x"), 0o664))

	require.NoError(t, fsops.SafeRemove(context.Background(), root, "gone.mkv"))

	_, err := os.Stat(filepath.Join(root, "gone.mkv"))
	require.True(t, os.IsNotExist(err))
}

func TestSafeRemoveRefusesToRemoveRootItself(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keepme.mkv"), []byte("x"), 0o664))

	for _, path := range []string{"", ".", ".."} {
		t.Run("path="+path, func(t *testing.T) {
			err := fsops.SafeRemove(context.Background(), root, path)
			require.ErrorIs(t, err, fsops.ErrOutsideRoot)
		})
	}

	_, statErr := os.Stat(root)
	require.NoError(t, statErr, "root itself must survive every refused call")
	_, statErr = os.Stat(filepath.Join(root, "keepme.mkv"))
	require.NoError(t, statErr, "root's contents must survive every refused call")
}

func TestSafeRemoveRefusesAnAbsolutePathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o664))

	err := fsops.SafeRemove(context.Background(), root, target)
	require.ErrorIs(t, err, fsops.ErrOutsideRoot)

	_, statErr := os.Stat(target)
	require.NoError(t, statErr, "the file outside root must survive")
}

func TestSafeRemoveReturnsQuicklyOnAPreCancelledContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "gone.mkv"), []byte("x"), 0o664))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := fsops.SafeRemove(ctx, root, "gone.mkv")
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(filepath.Join(root, "gone.mkv"))
	require.NoError(t, statErr, "a pre-cancelled context must do no work")
}
