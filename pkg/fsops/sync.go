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
)

// fileSyncFunc fsyncs an open file; overridden in white-box tests to make
// the fsync fail.
var fileSyncFunc = (*os.File).Sync

// SyncFile fsyncs the file at path, so a file another writer produced --
// a transcode's output, which FFmpeg's muxer closes itself and whose close
// error it does not report -- is on stable storage before a rename makes it
// the library's file. A rename is durable once its directory is synced
// ([MoveAtomic]), but that orders nothing about the file's own data: a
// crash after the rename could otherwise leave the library's name on a file
// with unwritten blocks. It also surfaces a delayed write error (Linux
// reports a writeback failure no descriptor has seen to the next fsync,
// even on a descriptor opened afterwards), which is the caller's cue to
// keep the original.
//
// The file is opened read-only: fsync(2) flushes the inode's dirty pages
// whatever the descriptor's access mode. Every error -- the open, the
// fsync, the close -- is returned, naming path.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("fsops: open %s for fsync: %w", path, err)
	}
	if err := fileSyncFunc(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsops: fsync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsops: close %s after fsync: %w", path, err)
	}
	return nil
}
