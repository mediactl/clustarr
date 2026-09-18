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
	"path/filepath"
	"strings"
	"time"
)

// Recycle moves path into root/<yyyy-mm-dd>/ (spec §11's recycle-bin
// layout), creating that directory if needed, and disambiguates a name
// collision by appending "-2", "-3", ... before the extension. It returns
// the final destination path.
func Recycle(root, path string) (string, error) {
	day := time.Now().UTC().Format("2006-01-02")
	destDir := filepath.Join(root, day)
	if err := os.MkdirAll(destDir, 0o775); err != nil {
		return "", fmt.Errorf("fsops: mkdir %s: %w", destDir, err)
	}

	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dest := filepath.Join(destDir, base)
	for n := 2; ; n++ {
		if _, err := os.Lstat(dest); os.IsNotExist(err) {
			break
		}
		dest = filepath.Join(destDir, fmt.Sprintf("%s-%d%s", stem, n, ext))
	}

	if err := MoveAtomic(path, dest); err != nil {
		return "", fmt.Errorf("fsops: recycle %s: %w", path, err)
	}
	return dest, nil
}

// SweepRecycleBin removes every dated subdirectory of root (the
// yyyy-mm-dd layout Recycle creates) whose date is before
// now.Add(-retention), returning how many it removed. now is a parameter,
// not time.Now(), so the sweeper is deterministically testable. A
// subdirectory name that does not parse as yyyy-mm-dd is left alone
// rather than guessed at, per amendment §A1.5's never-guess rule.
func SweepRecycleBin(root string, retention time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("fsops: readdir %s: %w", root, err)
	}

	cutoff := now.Add(-retention)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		day, err := time.Parse("2006-01-02", e.Name())
		if err != nil {
			continue // not one of ours; never guess, leave it alone
		}
		if day.Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return removed, fmt.Errorf("fsops: remove %s: %w", e.Name(), err)
			}
			removed++
		}
	}
	return removed, nil
}
