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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// CopyDir recursively copies src to dst. It walks src once to compute a
// total byte count, calls EnsureFreeSpace(dst, total), then walks it again
// copying files, calling progress after each one with the cumulative
// bytes copied and the precomputed total, and returning ctx.Err() as soon
// as ctx is cancelled between files.
func CopyDir(ctx context.Context, src, dst string, progress func(copiedBytes, totalBytes int64)) error {
	ctx, span := tracing.Start(ctx, "fsops.copy_dir")
	defer span.End()
	log := logging.FromContext(ctx)

	total, err := treeSize(src)
	if err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("fsops: size %s: %w", src, err)
	}
	// dst (and therefore the filesystem EnsureFreeSpace statfs(2)s) may not
	// exist yet -- CopyDir is frequently handed a not-yet-created target
	// root -- so create it before the space check rather than after, when
	// the walk below would otherwise be the first thing to create it.
	if err := os.MkdirAll(dst, 0o775); err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("fsops: mkdir %s: %w", dst, err)
	}
	if err := EnsureFreeSpace(dst, total); err != nil {
		tracing.RecordError(span, err)
		return err
	}

	var copied int64
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o775)
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		copied += info.Size()
		if progress != nil {
			progress(copied, total)
		}
		log.Debug("fsops: copied file", "path", rel, "copied_bytes", copied, "total_bytes", total)
		return nil
	})
	if walkErr != nil {
		tracing.RecordError(span, walkErr)
		return fmt.Errorf("fsops: copy tree %s to %s: %w", src, dst, walkErr)
	}
	return nil
}

func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
