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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// StrictlyUnder reports whether path names something inside root, and not
// root itself, once both are cleaned. Both must be absolute.
func StrictlyUnder(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PruneEmptyDirs removes dir and then each parent while it is empty,
// stopping at the first directory that is not empty (os.Remove refuses
// one) and never removing root or anything outside it. It removes nothing
// when anything between root and dir is not a directory -- a symbolic link
// above all, which os.Remove would unlink whatever its target holds, and
// through which a parent's removal would reach outside root.
func PruneEmptyDirs(root, dir string) {
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	if !StrictlyUnder(dir, root) {
		return
	}
	if missing, err := missingUnder(root, dir); err != nil || len(missing) > 0 {
		return
	}
	for StrictlyUnder(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ErrNotUnder is what [MkdirUnder] wraps when the directory it is asked
// for is not root or under it.
var ErrNotUnder = errors.New("fsops: not under the root")

// ErrNotDirectory is what [MkdirUnder] wraps when something that is not a
// directory stands between its root and the directory it is asked for: a
// file, or a symbolic link, live or dangling, which could lead the caller's
// move out of root.
var ErrNotDirectory = errors.New("fsops: not a directory")

// MkdirUnder creates dir and every missing directory between it and root,
// each with perm before the umask, and returns the topmost one it created
// -- "" when dir already existed -- so a caller whose next step fails can
// remove what it made and nothing else ([PruneMade]). dir must be root or
// under it ([ErrNotUnder]); root is the caller's bound, which must exist,
// and is never created or examined, so dir == root creates nothing. Every
// existing component strictly under root, dir included, must be a
// directory ([ErrNotDirectory]: a file, or a symbolic link anywhere on the
// way), with nothing created. A directory another writer creates meanwhile
// is taken as found, never reported as made. Each created directory's
// entry is fsynced in its parent, so a file moved into it afterwards does
// not outlive the folder across a crash.
func MkdirUnder(root, dir string, perm os.FileMode) (string, error) {
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	if dir == root && filepath.IsAbs(root) {
		return "", nil
	}
	if !StrictlyUnder(dir, root) {
		return "", fmt.Errorf("fsops: create %s: %w %s", dir, ErrNotUnder, root)
	}
	missing, err := missingUnder(root, dir)
	if err != nil {
		return "", fmt.Errorf("fsops: create %s: %w", dir, err)
	}
	made := ""
	for i, d := range missing {
		if err := os.Mkdir(d, perm); err != nil {
			if info, statErr := os.Lstat(d); errors.Is(err, fs.ErrExist) && statErr == nil && info.IsDir() {
				continue
			} else if errors.Is(err, fs.ErrExist) {
				err = fmt.Errorf("%w: %s", ErrNotDirectory, d)
			}
			PruneMade(made, missing[:i]...)
			return "", fmt.Errorf("fsops: create %s: %w", dir, err)
		}
		if made == "" {
			made = d
		}
		if err := fsyncDir(filepath.Dir(d)); err != nil {
			PruneMade(made, d)
			return "", err
		}
	}
	return made, nil
}

// missingUnder walks the components strictly under root down to dir, dir
// included, from the top, and returns the ones that do not exist yet,
// topmost first. Each existing one is read with Lstat before the next, so
// a symbolic link is caught where it stands and never followed: one, or a
// file, is [ErrNotDirectory]. dir must be strictly under root.
func missingUnder(root, dir string) ([]string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	d := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		d = filepath.Join(d, part)
		info, err := os.Lstat(d)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing := []string{d}
			for _, rest := range parts[i+1:] {
				missing = append(missing, filepath.Join(missing[len(missing)-1], rest))
			}
			return missing, nil
		case err != nil:
			return nil, err
		case !info.IsDir():
			return nil, fmt.Errorf("%w: %s", ErrNotDirectory, d)
		}
	}
	return nil, nil
}

// PruneMade removes what a [MkdirUnder] that returned made created: each
// of dirs (the deepest folder the caller used, typically the one it asked
// for) and its parents while they are empty, up to and including made and
// never above it. An empty made is a no-op: nothing was created.
func PruneMade(made string, dirs ...string) {
	if made == "" {
		return
	}
	for _, d := range dirs {
		PruneEmptyDirs(filepath.Dir(made), d)
	}
	PruneEmptyDirs(filepath.Dir(made), made)
}
