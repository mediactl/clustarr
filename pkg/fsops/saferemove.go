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
	"strings"
)

// ErrEscapesRoot is wrapped by SafeRemove when the resolved path would
// fall outside root.
var ErrEscapesRoot = errors.New("fsops: path escapes root")

// SafeRemove removes root/path (file or directory tree), refusing --
// returning an error wrapping ErrEscapesRoot -- when the resolved path
// would fall outside root, whether by a lexical ../ escape or because a
// directory component on the path is a symlink whose real target lies
// outside root.
func SafeRemove(root, path string) error {
	root = filepath.Clean(root)
	full := filepath.Join(root, path)

	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrEscapesRoot, path)
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
		return fmt.Errorf("%w: %s resolves outside %s via a symlink", ErrEscapesRoot, path, root)
	}

	if err := os.RemoveAll(full); err != nil {
		return fmt.Errorf("fsops: remove %s: %w", full, err)
	}
	return nil
}
