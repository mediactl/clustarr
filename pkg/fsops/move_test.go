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

// A directory crosses filesystems too: the usenet and torrent engines
// publish a whole transfer directory from a scratch volume onto /data
// (2026-09-30: every non-path scratch placement failed the publish with
// EISDIR). It is copied whole under .partial -- clearing one a failed
// attempt left -- and renamed into place, so dst appears only complete.
func TestMoveAtomicMovesADirectoryAcrossFilesystems(t *testing.T) {
	old := renameFunc
	t.Cleanup(func() { renameFunc = old })
	calls := 0
	renameFunc = func(o, n string) error {
		calls++
		if calls == 1 {
			return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
		}
		return os.Rename(o, n)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "scratch", "movie")
	dst := filepath.Join(dir, "data", "movie")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "Subs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "movie.mkv"), []byte("video"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "Subs", "en.srt"), []byte("subs"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dst+".partial", "stale"), 0o755))

	require.NoError(t, MoveAtomic(src, dst))

	got, err := os.ReadFile(filepath.Join(dst, "movie.mkv"))
	require.NoError(t, err)
	require.Equal(t, "video", string(got))
	got, err = os.ReadFile(filepath.Join(dst, "Subs", "en.srt"))
	require.NoError(t, err)
	require.Equal(t, "subs", string(got))
	_, err = os.Stat(filepath.Join(dst, "stale"))
	require.True(t, os.IsNotExist(err), "a stale .partial is cleared, not merged")
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err), "the source directory is removed")
	_, err = os.Stat(dst + ".partial")
	require.True(t, os.IsNotExist(err))
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

// A concurrent caller (or a retried NFS REMOVE) unlinks src between this
// call's link and its unlink: the move is complete, and dst -- by then the
// file's only name -- must survive.
func TestMoveNoReplaceTreatsAVanishedSourceAsAFinishedMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	old := linkFunc
	t.Cleanup(func() { linkFunc = old })
	linkFunc = func(oldname, newname string) error {
		if err := os.Link(oldname, newname); err != nil {
			return err
		}
		return os.Remove(oldname) // the other caller wins the unlink
	}

	require.NoError(t, MoveNoReplace(src, dst))

	_, err := os.Lstat(src)
	require.True(t, os.IsNotExist(err), "the source is gone")
	got, err := os.ReadFile(dst)
	require.NoError(t, err, "dst is the file's last name and must survive")
	require.Equal(t, "payload", string(got))
}

// The unlink of src fails for another reason: dst is rolled back only while
// src still names the same file, so the file always keeps a name.
func TestMoveNoReplaceRollsBackOnlyWhileTheSourceIsTheSameFile(t *testing.T) {
	cases := []struct {
		name string
		// unlink runs in place of removing src and returns its error.
		unlink     func(t *testing.T, src string) error
		wantDst    bool
		wantSrc    string
		wantDstStr string
	}{
		{
			name:    "src still the same file: dst removed",
			unlink:  func(*testing.T, string) error { return syscall.EACCES },
			wantDst: false,
			wantSrc: "payload",
		},
		{
			name: "src replaced by another file: both names kept",
			unlink: func(t *testing.T, src string) error {
				require.NoError(t, os.Remove(src))
				require.NoError(t, os.WriteFile(src, []byte("another"), 0o664))
				return syscall.EIO
			},
			wantDst:    true,
			wantSrc:    "another",
			wantDstStr: "payload",
		},
		{
			name: "src gone behind a non-ENOENT error: dst kept",
			unlink: func(t *testing.T, src string) error {
				require.NoError(t, os.Remove(src))
				return syscall.EIO
			},
			wantDst:    true,
			wantDstStr: "payload",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.mkv")
			dst := filepath.Join(dir, "dst.mkv")
			require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

			old := removeFunc
			t.Cleanup(func() { removeFunc = old })
			removeFunc = func(name string) error {
				require.Equal(t, src, name)
				return tc.unlink(t, name)
			}

			require.Error(t, MoveNoReplace(src, dst))

			got, err := os.ReadFile(dst)
			if tc.wantDst {
				require.NoError(t, err, "dst must be kept")
				require.Equal(t, tc.wantDstStr, string(got))
			} else {
				require.True(t, os.IsNotExist(err), "dst must be rolled back")
			}
			got, err = os.ReadFile(src)
			if tc.wantSrc == "" {
				require.True(t, os.IsNotExist(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantSrc, string(got))
			}
		})
	}
}
