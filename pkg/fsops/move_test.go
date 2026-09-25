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
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMoveAtomicRenamesWithinOneFilesystem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	require.NoError(t, MoveAtomic(src, dst))

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "source must be gone")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
}

func TestMoveAtomicFallsBackOnEXDEVAndLeavesNoPartial(t *testing.T) {
	old := renameFunc
	t.Cleanup(func() { renameFunc = old })
	calls := 0
	renameFunc = func(o, n string) error {
		calls++
		if calls == 1 {
			return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
		}
		return os.Rename(o, n) // the .partial -> dst rename must still use the real one
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	require.NoError(t, MoveAtomic(src, dst))

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "source must be removed after the EXDEV fallback")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
	_, err = os.Stat(dst + ".partial")
	require.True(t, os.IsNotExist(err), ".partial must not remain")
}

func TestMoveNoReplaceMovesAFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	require.NoError(t, MoveNoReplace(src, dst))

	_, err := os.Lstat(src)
	require.True(t, os.IsNotExist(err), "the source must be gone")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
}

// The point of MoveNoReplace: rename(2) would replace dst here.
func TestMoveNoReplaceRefusesAnExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))
	require.NoError(t, os.WriteFile(dst, []byte("already here"), 0o664))

	err := MoveNoReplace(src, dst)
	require.ErrorIs(t, err, ErrExists)

	got, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got), "the source is untouched")
	got, err = os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "already here", string(got), "the existing target is untouched")
}

// A symlink at dst is something at dst: refused, not followed.
func TestMoveNoReplaceRefusesADanglingSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), dst))

	require.ErrorIs(t, MoveNoReplace(src, dst), ErrExists)
	_, err := os.Lstat(src)
	require.NoError(t, err)
}

// A move interrupted between its link and its unlink leaves dst a hard link
// to src; running it again finishes it rather than reporting a collision.
func TestMoveNoReplaceFinishesAnInterruptedMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))
	require.NoError(t, os.Link(src, dst))

	require.NoError(t, MoveNoReplace(src, dst))

	_, err := os.Lstat(src)
	require.True(t, os.IsNotExist(err), "the source must be gone")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
}
