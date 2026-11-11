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
// one) and never removing root or anything outside it.
func PruneEmptyDirs(root, dir string) {
	for StrictlyUnder(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ErrNotUnder is what [MkdirUnder] wraps when the directory it is asked
// for is not strictly under its root.
var ErrNotUnder = errors.New("fsops: not strictly under the root")

// MkdirUnder creates dir and every missing directory between it and root,
// each with perm before the umask, and returns the topmost one it created
// -- "" when dir already existed -- so a caller whose next step fails can
// remove what it made and nothing else ([PruneMade]). dir must be strictly
// under root ([ErrNotUnder]) and root must exist; an existing component
// that is not a directory -- a symbolic link included, which could lead the
// caller's move out of root -- is an error, with nothing created. A
// directory another writer creates meanwhile is taken as found, never
// reported as made. Each created directory's entry is fsynced in its
// parent, so a file moved into it afterwards does not outlive the folder
// across a crash.
func MkdirUnder(root, dir string, perm os.FileMode) (string, error) {
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	if !StrictlyUnder(dir, root) {
		return "", fmt.Errorf("fsops: create %s: %w %s", dir, ErrNotUnder, root)
	}
	var missing []string // deepest first
	for d := dir; StrictlyUnder(d, root); d = filepath.Dir(d) {
		info, err := os.Lstat(d)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("fsops: create %s: %s is not a directory", dir, d)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("fsops: create %s: %w", dir, err)
		}
		missing = append(missing, d)
	}
	made := ""
	for i := len(missing) - 1; i >= 0; i-- {
		d := missing[i]
		if err := os.Mkdir(d, perm); err != nil {
			if info, statErr := os.Lstat(d); errors.Is(err, fs.ErrExist) && statErr == nil && info.IsDir() {
				continue
			}
			PruneMade(made, missing[i+1:]...)
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
