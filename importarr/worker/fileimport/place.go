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

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// errWouldOverwrite is a destination that already holds a different file
// which could not be put in the recycle bin first: importing over it would
// destroy it, so the file is rejected instead.
var errWouldOverwrite = errors.New("would overwrite")

// placeFile imports src (whose stat is srcInfo) to dest, never destroying
// what dest already holds.
//
// fsops.Import renames a copy over an existing dest, so a file already at a
// library path -- the previous import of the same item under the same
// rendered name, which an upgrade or a manual re-import replaces -- would be
// gone with no copy in the recycle bin, unlike one replaced at a different
// path. It is linked into the bin first (fsops.RecycleLink), and dest then
// names a complete file at every moment: the old one until the rename, the
// new one after. A dest that already IS src (a redelivery after the link
// landed) is left as it is; re-importing it would turn a hard link into a
// copy and bin a file that is not being replaced.
func placeFile(ctx context.Context, bin, src string, srcInfo os.FileInfo, dest string, mode fsops.ImportMode) error {
	if di, err := os.Stat(dest); err == nil {
		if os.SameFile(di, srcInfo) {
			return nil
		}
		recycled, rerr := fsops.RecycleLink(fsops.RecycleBinPath(bin), dest)
		if rerr != nil {
			return fmt.Errorf("%w %s, which could not be put in the recycle bin first: %w", errWouldOverwrite, dest, rerr)
		}
		logging.FromContext(ctx).Info("fileimport: recycled the file an import replaces in place", "path", dest, "recycled", recycled)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fileimport: stat %s: %w", dest, err)
	}
	if err := fsops.Import(ctx, src, dest, mode); err != nil {
		return fmt.Errorf("fileimport: import %s: %w", filepath.Base(src), err)
	}
	return nil
}

// countMedia counts the plain media files (fsops.ClassMedia) a walk of root
// under c visits, stopping once it reaches upTo. It is a classification-only
// pass, with no side effect; an unreadable entry is not counted (the import
// walk itself reports it).
func countMedia(ctx context.Context, c fsops.Classifier, root string, upTo int) (int, error) {
	n := 0
	err := c.Walk(ctx, root, func(_ string, _ os.FileInfo, class fsops.FileClass) error {
		if class == fsops.ClassMedia {
			n++
			if n >= upTo {
				return filepath.SkipAll
			}
		}
		return nil
	}, func(string, error) error { return nil })
	if err != nil {
		return 0, fmt.Errorf("fileimport: count media under %s: %w", root, err)
	}
	return n, nil
}
