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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// renameFunc renames a file; overridden in white-box tests to simulate
// EXDEV without needing two real filesystems in the test environment.
var renameFunc = os.Rename

// removeFunc unlinks [MoveNoReplace]'s source; overridden in white-box tests
// to make that unlink fail.
var removeFunc = os.Remove

// MoveAtomic moves src -- a file or a directory -- to dst: rename(2) when
// both are on the same filesystem, or copy-then-rename-then-remove-source
// when they are not (EXDEV), finishing with an fsync of dst's parent
// directory.
func MoveAtomic(src, dst string) error {
	if err := renameFunc(src, dst); err == nil {
		return fsyncDir(filepath.Dir(dst))
	} else if !isEXDEV(err) {
		return fmt.Errorf("fsops: rename %s to %s: %w", src, dst, err)
	}

	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("fsops: stat %s: %w", src, err)
	}
	tmp := dst + ".partial"
	if info.IsDir() {
		// A whole transfer directory from a scratch volume onto the data
		// volume (both engines' publish). It is copied under .partial --
		// clearing one an interrupted attempt left, never merging into it --
		// then renamed, so dst appears only complete: an engine that finds
		// dst after a restart may trust it.
		if err := os.RemoveAll(tmp); err != nil {
			return fmt.Errorf("fsops: clear %s: %w", tmp, err)
		}
		if err := CopyDir(context.Background(), src, tmp, nil); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("fsops: copy %s to %s: %w", src, tmp, err)
		}
	} else if err := copyFile(context.Background(), src, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: copy %s to %s: %w", src, tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("fsops: rename %s to %s: %w", tmp, dst, err)
	}
	if err := fsyncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("fsops: remove source %s after move: %w", src, err)
	}
	return nil
}

// ErrExists is what [MoveNoReplace] wraps when something already exists at
// its destination.
var ErrExists = errors.New("fsops: destination exists")

// MoveNoReplace moves src to dst on one filesystem and never replaces dst.
// [MoveAtomic]'s rename(2) silently replaces an existing dst, so a caller
// that checked dst was free and then moved could still clobber a file that
// arrived in between; link(2) refuses an existing dst atomically (EEXIST),
// on NFS too. The move is link, then unlink src, then an fsync of dst's
// parent directory (and src's, when it differs), as MoveAtomic does.
//
// An existing dst is reported as an error wrapping [ErrExists], with
// nothing changed -- unless dst is already a hard link to src: that is a
// MoveNoReplace interrupted between its link and its unlink (or an NFS
// LINK retried after its reply was lost), so the move is finished instead.
// There is no cross-device fallback: link(2) fails with EXDEV, reported as
// is.
//
// A failed unlink of src never costs the file its last name. A src that is
// already gone means the move completed -- two callers that both found the
// interrupted move above both unlink src, and one loses; an NFS REMOVE
// retried after its reply was lost reports ENOENT too -- so that is
// success. Any other failure removes dst again only while src still names
// the same file; otherwise both names are left for the next call to finish.
func MoveNoReplace(src, dst string) error {
	if err := linkFunc(src, dst); err != nil {
		if !errors.Is(err, fs.ErrExist) || !sameFile(src, dst) {
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("fsops: move %s to %s: %w: %w", src, dst, ErrExists, err)
			}
			return fmt.Errorf("fsops: move %s to %s: %w", src, dst, err)
		}
	}
	if err := removeFunc(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Leave the one name the caller started with, but only while src
		// still is that file: removing dst otherwise could remove the
		// file's last name.
		if sameFile(src, dst) {
			_ = os.Remove(dst)
		}
		return fmt.Errorf("fsops: move %s to %s: remove the source: %w", src, dst, err)
	}
	if err := fsyncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if srcDir := filepath.Dir(src); srcDir != filepath.Dir(dst) {
		return fsyncDir(srcDir)
	}
	return nil
}

// sameFile reports whether a and b both exist and name the same file.
func sameFile(a, b string) bool {
	ai, err := os.Lstat(a)
	if err != nil {
		return false
	}
	bi, err := os.Lstat(b)
	return err == nil && os.SameFile(ai, bi)
}
