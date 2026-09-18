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
	"syscall"
)

// Usage is a filesystem's space accounting for the mount backing a path.
type Usage struct{ Total, Free, Available int64 }

// DiskUsage statfs(2)s the filesystem backing path.
//
// syscall.Statfs_t's field widths vary slightly by GOARCH (Bsize is int64
// on linux/amd64 and linux/arm64, which are the only targets
// images/Dockerfile.media* build); the explicit int64(...) casts keep the
// arithmetic portable across both without a build-tag split.
func DiskUsage(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, fmt.Errorf("fsops: statfs %s: %w", path, err)
	}
	bsize := int64(st.Bsize)
	return Usage{
		Total:     int64(st.Blocks) * bsize,
		Free:      int64(st.Bfree) * bsize,
		Available: int64(st.Bavail) * bsize,
	}, nil
}

// FreeBytes is DiskUsage(path).Free -- the one field spec §7 names
// directly.
func FreeBytes(path string) (int64, error) {
	u, err := DiskUsage(path)
	if err != nil {
		return 0, err
	}
	return u.Free, nil
}

// ErrInsufficientSpace is wrapped by EnsureFreeSpace when path's
// filesystem does not have enough free space.
var ErrInsufficientSpace = errors.New("fsops: insufficient free space")

// EnsureFreeSpace returns an error wrapping ErrInsufficientSpace unless
// path's filesystem has at least needed bytes free. needed <= 0 always
// passes, mirroring RootFolderSpec.MinFreeBytes=0's "check disabled"
// convention.
func EnsureFreeSpace(path string, needed int64) error {
	if needed <= 0 {
		return nil
	}
	free, err := FreeBytes(path)
	if err != nil {
		return err
	}
	if free < needed {
		return fmt.Errorf("%w: %s has %d bytes, need %d", ErrInsufficientSpace, path, free, needed)
	}
	return nil
}
