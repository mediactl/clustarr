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
	"fmt"
	"os"
	"path/filepath"
)

// renameFunc renames a file; overridden in white-box tests to simulate
// EXDEV without needing two real filesystems in the test environment.
var renameFunc = os.Rename

// MoveAtomic moves src to dst: rename(2) when both are on the same
// filesystem, or copy-then-rename-then-remove-source when they are not
// (EXDEV), finishing with an fsync of dst's parent directory.
func MoveAtomic(src, dst string) error {
	if err := renameFunc(src, dst); err == nil {
		return fsyncDir(filepath.Dir(dst))
	} else if !isEXDEV(err) {
		return fmt.Errorf("fsops: rename %s to %s: %w", src, dst, err)
	}

	tmp := dst + ".partial"
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fsops: copy %s to %s: %w", src, tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fsops: rename %s to %s: %w", tmp, dst, err)
	}
	if err := fsyncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("fsops: remove source %s after move: %w", src, err)
	}
	return nil
}
