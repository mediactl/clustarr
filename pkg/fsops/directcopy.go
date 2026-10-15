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
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A copy across filesystems -- MoveAtomic's EXDEV fallback, CopyDir,
// HardlinkOrCopy's -- moves a whole transfer or a transcode's output from a
// scratch volume onto /data, and /data is NFS. A buffered copy leaves the
// data in the page cache, dirty and then under writeback until the NAS
// takes it, and Linux's NFS client has no per-cgroup writeback throttling,
// so the copy fills the pod's memory: on 2026-10-15 a buffered 3 GiB copy
// onto the Synology held 1.9 GiB in writeback, pinned a 2 GiB pod at its
// limit and crawled at 8.3 MB/s in reclaim, and three transcode pool pods
// (4 GiB) were OOM-killed staging their outputs. So the destination is
// written with O_DIRECT, from one aligned buffer -- like a file served over
// HTTP with sendfile(2), the data never waits in memory -- and the source's
// pages are dropped behind the read. A filesystem that refuses O_DIRECT
// gets a buffered copy that fsyncs every syncEvery bytes instead.

// copyChunk is one read and one write of a copy. NFS commits each O_DIRECT
// write before it returns, so a larger one carries fewer commits: the
// Synology took 16 MB/s in 8 MiB writes and 28 MB/s in 64 MiB ones.
var copyChunk = 64 << 20

// syncEvery is how much a buffered copy writes between fsyncs.
const syncEvery = 256 << 20

// directAlign is the alignment O_DIRECT asks of a buffer, an offset and a
// length on a local filesystem (NFS asks none).
const directAlign = 4096

// openDirect opens path for writing with O_DIRECT; a variable so a test can
// refuse it as a filesystem without O_DIRECT does (EINVAL).
var openDirect = func(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_DIRECT, perm)
}

// copyStats is how a copy wrote, for the tests.
type copyStats struct{ direct bool }

// copyFile copies src to dst through dst+".partial", creating dst's parent
// directory and keeping src's permission bits: the partial is fsynced and
// renamed onto dst and dst's directory fsynced, as AtomicWrite does, so dst
// appears only whole. ctx is checked between chunks, and any failure removes
// the partial.
func copyFile(ctx context.Context, src, dst string) error {
	_, err := copyFileStats(ctx, src, dst)
	return err
}

func copyFileStats(ctx context.Context, src, dst string) (copyStats, error) {
	var st copyStats
	in, err := os.Open(src)
	if err != nil {
		return st, fmt.Errorf("fsops: open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return st, fmt.Errorf("fsops: stat %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return st, fmt.Errorf("fsops: mkdir %s: %w", filepath.Dir(dst), err)
	}
	perm := info.Mode().Perm()
	tmp := dst + ".partial"
	out, err := openDirect(tmp, perm)
	if err == nil {
		st.direct = true
	} else if errors.Is(err, syscall.EINVAL) {
		out, err = os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	}
	if err != nil {
		return st, fmt.Errorf("fsops: create %s: %w", tmp, err)
	}
	fail := func(err error) (copyStats, error) {
		_ = out.Close()
		_ = os.Remove(tmp)
		return st, err
	}
	// O_CREATE applies perm only to a new file, and the umask masks it: a
	// partial an interrupted copy left would donate its own mode.
	if err := out.Chmod(perm); err != nil {
		return fail(fmt.Errorf("fsops: chmod %s: %w", tmp, err))
	}

	buf := alignedBuffer(copyChunk)
	direct := st.direct
	var off, synced int64
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			// O_DIRECT writes whole blocks on a local filesystem: a tail
			// that is not one is written through the cache.
			if direct && n%directAlign != 0 {
				if err := setDirect(out, false); err != nil {
					return fail(fmt.Errorf("fsops: write %s: %w", tmp, err))
				}
				direct = false
			}
			if _, err := out.Write(buf[:n]); err != nil {
				return fail(fmt.Errorf("fsops: write %s: %w", tmp, err))
			}
			// The source's pages are clean and done with: drop them, so the
			// copy holds no more of the pod's memory than its buffer.
			_ = unix.Fadvise(int(in.Fd()), off, int64(n), unix.FADV_DONTNEED)
			off += int64(n)
			if !st.direct && off-synced >= syncEvery {
				if err := out.Sync(); err != nil {
					return fail(fmt.Errorf("fsops: fsync %s: %w", tmp, err))
				}
				synced = off
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return fail(fmt.Errorf("fsops: read %s: %w", src, rerr))
		}
	}
	if err := out.Sync(); err != nil {
		return fail(fmt.Errorf("fsops: fsync %s: %w", tmp, err))
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return st, fmt.Errorf("fsops: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return st, fmt.Errorf("fsops: rename %s to %s: %w", tmp, dst, err)
	}
	return st, fsyncDir(filepath.Dir(dst))
}

// alignedBuffer is n bytes starting on a directAlign boundary.
func alignedBuffer(n int) []byte {
	b := make([]byte, n+directAlign)
	off := int(uintptr(unsafe.Pointer(&b[0])) & (directAlign - 1))
	if off != 0 {
		off = directAlign - off
	}
	return b[off : off+n]
}

// setDirect turns O_DIRECT on or off for f (fcntl F_SETFL).
func setDirect(f *os.File, on bool) error {
	fd := int(f.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if on {
		flags |= syscall.O_DIRECT
	} else {
		flags &^= syscall.O_DIRECT
	}
	_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
	return err
}
