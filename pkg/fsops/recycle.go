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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRecycleBin is RootFolderSpec.RecycleBin.Path's +kubebuilder:default,
// restated for an object that never went through the apiserver's defaulting
// (one built in Go). TestDefaultRecycleBinIsTheCRDs pins the two together.
const DefaultRecycleBin = "/data/.recycle"

// RecycleBinPath is a RootFolder's recycle-bin path with the CRD default
// applied: path, or [DefaultRecycleBin] when it is empty. Recycling into an
// empty root would put the bin's dated folders in the process's working
// directory.
func RecycleBinPath(path string) string {
	if path == "" {
		return DefaultRecycleBin
	}
	return path
}

// Recycle moves path into root/<yyyy-mm-dd>/ (spec §11's recycle-bin
// layout), creating that directory if needed, and disambiguates a name
// collision by appending "-2", "-3", ... before the extension. It returns
// the final destination path.
func Recycle(root, path string) (string, error) {
	dest, err := recycleDest(root, path)
	if err != nil {
		return "", err
	}
	if err := MoveAtomic(path, dest); err != nil {
		return "", fmt.Errorf("fsops: recycle %s: %w", path, err)
	}
	return dest, nil
}

// RecycleLink puts a copy of path into the recycle bin -- the same
// root/<yyyy-mm-dd>/ layout and collision rule as [Recycle] -- WITHOUT
// removing path: a hard link when root shares path's filesystem, a full
// copy otherwise ([HardlinkOrCopy]). It returns the destination.
//
// It exists for replace-in-place, where the caller is about to rename a new
// file over path. Recycling by move first and renaming second leaves a
// window in which path does not exist at all, so a crash between the two
// strands the library with a hole where the file was. Linking first and
// then renaming over path means path always names a complete file: the old
// one until the rename, the new one after, with the old one already safe in
// the bin.
func RecycleLink(root, path string) (string, error) {
	dest, err := recycleDest(root, path)
	if err != nil {
		return "", err
	}
	if _, err := HardlinkOrCopy(path, dest); err != nil {
		return "", fmt.Errorf("fsops: recycle %s: %w", path, err)
	}
	if err := fsyncDir(filepath.Dir(dest)); err != nil {
		return "", err
	}
	return dest, nil
}

// recycleDest is the destination [Recycle] and [RecycleLink] use for path:
// root/<yyyy-mm-dd>/<base>, created if needed, suffixed "-2", "-3", ...
// before the extension on a collision.
func recycleDest(root, path string) (string, error) {
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
	return dest, nil
}

// SweepRecycleBin removes every dated subdirectory of root (the
// yyyy-mm-dd layout Recycle creates) whose whole day ended at least
// retention before now, returning how many it removed. The folder of a day
// holds files recycled at any time that day, up to its last second, so a
// folder is kept until retention has passed since the END of its day: no
// file is removed sooner than retention after it was recycled (Sonarr's
// cleanup keeps a folder until its last write is cleanupDays old, which
// the day's end bounds). Comparing the day's START with now - retention
// removed a file recycled late on a day up to a day early. now is a
// parameter, not time.Now(), so the sweeper is deterministically testable. A
// subdirectory name that does not parse as yyyy-mm-dd is left alone
// rather than guessed at, per amendment §A1.5's never-guess rule.
//
// A day's folder that cannot be removed does not stop the sweep: the days
// after it are removed all the same, and the error returned at the end
// names the first that was left and how many were. A folder a recycled
// download left without its owner's write bit is given it and removed
// (removeAllOwned). On talos one file under 2026-10-22, "permission
// denied", failed every sweep from then on at that folder, so nothing
// recycled after it was ever removed and the task was dead-lettered twice
// an hour (found 2026-11-28).
//
// ctx is checked once before reading root and once per directory entry,
// so a large bin stops promptly on cancellation instead of finishing the
// whole sweep; the returned error wraps ctx's error so
// errors.Is(err, context.Canceled) (or context.DeadlineExceeded) holds.
// removed still reports how many entries were removed before
// cancellation was observed.
func SweepRecycleBin(ctx context.Context, root string, retention time.Duration, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("fsops: sweep recycle bin %s: %w", root, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("fsops: readdir %s: %w", root, err)
	}

	cutoff := now.Add(-retention)
	removed := 0
	var (
		left     int
		firstErr error
	)
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return removed, fmt.Errorf("fsops: sweep recycle bin %s: %w", root, ctxErr)
		}
		if !e.IsDir() {
			continue
		}
		day, err := time.Parse("2006-01-02", e.Name())
		if err != nil {
			continue // not one of ours; never guess, leave it alone
		}
		if !day.AddDate(0, 0, 1).After(cutoff) {
			if err := removeAllOwned(filepath.Join(root, e.Name())); err != nil {
				if left++; firstErr == nil {
					firstErr = fmt.Errorf("fsops: remove %s: %w", e.Name(), err)
				}
				continue
			}
			removed++
		}
	}
	if left > 1 {
		return removed, fmt.Errorf("%w (and %d more folders were left)", firstErr, left-1)
	}
	return removed, firstErr
}

// removeAllOwned is os.RemoveAll for a tree that may hold directories
// without their owner's write bit, which unlinking their entries needs: a
// download recycled whole keeps the modes its client gave it. On a
// permission error every directory of the tree is given rwx for its owner
// (what `go clean -modcache` does for the module cache) and the removal is
// tried once more. A tree another user owns still fails, with that error.
func removeAllOwned(path string) error {
	err := removeAllFunc(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
		if werr == nil && d.IsDir() {
			if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
	return removeAllFunc(path)
}

// removeAllFunc is os.RemoveAll, a seam for a tree no chmod makes removable.
var removeAllFunc = os.RemoveAll
