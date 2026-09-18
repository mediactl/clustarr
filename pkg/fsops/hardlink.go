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
			_ = os.Remove(dst)
			return false, fmt.Errorf("fsops: %s reports link count %d after linking %s, want >= 2", dst, st.Nlink, src)
		}
		return true, nil
	}
	if err := copyFile(context.Background(), src, dst); err != nil {
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
//
// ctx is checked between chunks (see ctxReader) via AtomicWrite's
// io.Copy, so a long copy notices cancellation mid-transfer -- and, via
// AtomicWrite's own cleanup-on-error path, never leaves dst's ".partial"
// behind -- instead of only before the copy starts or after it finishes.
// context.Background() is a valid ctx for copyFile's two ctx-less spec
// §7 callers (HardlinkOrCopy's fallback, MoveAtomic's EXDEV path), which
// simply never cancel.
func copyFile(ctx context.Context, src, dst string) error {
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
	return AtomicWrite(dst, &ctxReader{ctx: ctx, r: in}, info.Mode().Perm())
}

// ctxReader makes r's Read calls observe ctx. Wrapping in as a plain
// io.Reader (rather than passing the *os.File through directly) also
// defeats io.Copy's io.ReaderFrom/io.WriterTo fast paths -- notably
// *os.File-to-*os.File copy_file_range -- forcing the generic, buffered
// copyBuffer loop, which is what actually calls Read (and therefore
// checks ctx) more than once for a multi-chunk file instead of handing
// the whole transfer to the kernel in one call.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}
