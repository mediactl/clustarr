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
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// linkFunc creates a hard link; overridden in white-box tests to simulate
// EXDEV without needing two real filesystems in the test environment.
var linkFunc = os.Link

// HardlinkOrCopy links dst to src, or copies src to dst when linking is
// not possible (different filesystems -- EXDEV -- or any other link
// failure). linked reports which happened. On a successful link it
// verifies the resulting link count is >= 2, guarding against a linker
// that reports success without the inode actually being shared.
func HardlinkOrCopy(src, dst string) (bool, error) {
	if err := linkFunc(src, dst); err == nil {
		info, statErr := os.Stat(dst)
		if statErr != nil {
			return true, fmt.Errorf("fsops: stat %s after linking: %w", dst, statErr)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink < 2 {
			return false, fmt.Errorf("fsops: %s reports link count %d after linking %s, want >= 2", dst, st.Nlink, src)
		}
		return true, nil
	}
	if err := copyFile(src, dst); err != nil {
		return false, fmt.Errorf("fsops: copy fallback for %s to %s: %w", src, dst, err)
	}
	return false, nil
}

// isEXDEV reports whether err is a cross-device link failure.
func isEXDEV(err error) bool {
	var linkErr *os.LinkError
	return errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV)
}

// copyFile copies src to dst, creating dst's parent directory and
// preserving src's permission bits, via AtomicWrite so a copy failure
// never leaves a partial dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("fsops: open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("fsops: stat %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return fmt.Errorf("fsops: mkdir %s: %w", filepath.Dir(dst), err)
	}
	return AtomicWrite(dst, in, info.Mode().Perm())
}
