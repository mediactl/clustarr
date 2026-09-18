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
	"io"
	"os"
	"path/filepath"
)

// AtomicWrite writes r to path without ever exposing a partial file at
// path: it writes to path+".partial" in path's own directory, fsyncs that
// file and its directory, then renames it onto path. Any failure along
// the way removes the .partial file first.
//
// mode is the mode the finished file carries, unconditionally: O_CREATE
// applies a mode only when it creates the file, so a .partial left behind
// by a crashed write would otherwise donate its own permissions to the
// result, and the process umask would mask the rest. The explicit Chmod
// below defeats both -- callers pass the mode that has to end up on disk
// (RootFolderSpec.Perms.FileMode, "0664" by default, so a media server
// running as another uid in the media group can read what we wrote).
func AtomicWrite(path string, r io.Reader, mode os.FileMode) error {
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("fsops: create %s: %w", tmp, err)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: chmod %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsops: rename %s to %s: %w", tmp, path, err)
	}
	return fsyncDir(filepath.Dir(path))
}

// fsyncDir fsyncs a directory so a preceding rename(2) into it is durable.
// Shared by atomic.go, move.go and recycle.go.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("fsops: open %s for fsync: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsops: fsync %s: %w", dir, err)
	}
	return nil
}
