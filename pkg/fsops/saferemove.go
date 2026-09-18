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
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot is wrapped by SafeRemove when the resolved path would
// fall outside root, or would resolve to root itself -- removing the
// recycle/library root out from under every other reader is never a
// valid caller intent for this guard, so an empty path, ".", or a path
// that Clean+Rel reduce to root is refused the same as an outright
// escape.
var ErrOutsideRoot = errors.New("fsops: path resolves outside root")

// SafeRemove removes root/path (file or directory tree), refusing --
// returning an error wrapping ErrOutsideRoot -- when the resolved path
// would fall outside root or resolve to root itself: an empty path, ".",
// a lexical ../ escape, an absolute path outside root, or a directory
// component on the path that is a symlink whose real target lies
// outside root.
//
// path may be relative (joined onto root) or absolute (validated
// against root as given, never silently re-rooted underneath it, so an
// absolute path outside root is refused rather than being reinterpreted
// as a same-named subpath of root).
//
// ctx is checked once before any filesystem work. SafeRemove has no
// internal per-entry walk to check against -- os.RemoveAll performs its
// own recursive delete once the boundary check passes -- so this single
// check is the only cancellation point this function can usefully offer.
// The returned error wraps ctx's error so errors.Is(err,
// context.Canceled) (or context.DeadlineExceeded) holds.
func SafeRemove(ctx context.Context, root, path string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fsops: safe remove %s: %w", path, err)
	}

	root = filepath.Clean(root)
	var full string
	if filepath.IsAbs(path) {
		full = filepath.Clean(path)
	} else {
		full = filepath.Join(root, path)
	}

	rel, err := filepath.Rel(root, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrOutsideRoot, path)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("fsops: resolve root %s: %w", root, err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return fmt.Errorf("fsops: resolve %s: %w", filepath.Dir(full), err)
	}
	if realParent != realRoot && !strings.HasPrefix(realParent, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s resolves outside %s via a symlink", ErrOutsideRoot, path, root)
	}

	if err := os.RemoveAll(full); err != nil {
		return fmt.Errorf("fsops: remove %s: %w", full, err)
	}
	return nil
}
