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
	"os"
	"path/filepath"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ImportMode selects how Import transfers a file from src to dst.
type ImportMode int

const (
	ImportHardlink ImportMode = iota // hardlink; falls back to copy on any link failure (EXDEV or otherwise)
	ImportCopy
	ImportMove
)

func (m ImportMode) String() string {
	switch m {
	case ImportHardlink:
		return "hardlink"
	case ImportCopy:
		return "copy"
	case ImportMove:
		return "move"
	default:
		return fmt.Sprintf("ImportMode(%d)", int(m))
	}
}

// Import transfers src to dst per mode, creating dst's parent directory
// (0775) first if it does not exist. It is the ctx-aware, traced,
// free-space-checked wrapper the spec's lower-level primitives
// (HardlinkOrCopy, MoveAtomic) intentionally lack, since those two keep
// the spec §7 signature exactly (no context.Context parameter) and this
// is where importarr's fileimport worker should call in.
func Import(ctx context.Context, src, dst string, mode ImportMode) error {
	log := logging.FromContext(ctx)
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return fmt.Errorf("fsops: mkdir %s: %w", filepath.Dir(dst), err)
	}

	switch mode {
	case ImportHardlink:
		linked, err := HardlinkOrCopy(src, dst)
		if err != nil {
			return fmt.Errorf("fsops: import %s: %w", src, err)
		}
		log.Debug("fsops: imported", "src", src, "dst", dst, "mode", mode.String(), "linked", linked)
		return nil

	case ImportCopy:
		_, span := tracing.Start(ctx, "fsops.import_copy")
		defer span.End()
		info, err := os.Stat(src)
		if err != nil {
			tracing.RecordError(span, err)
			return fmt.Errorf("fsops: stat %s: %w", src, err)
		}
		if err := EnsureFreeSpace(filepath.Dir(dst), info.Size()); err != nil {
			tracing.RecordError(span, err)
			return err
		}
		if err := copyFile(ctx, src, dst); err != nil {
			tracing.RecordError(span, err)
			return fmt.Errorf("fsops: import %s: %w", src, err)
		}
		log.Debug("fsops: imported", "src", src, "dst", dst, "mode", mode.String())
		return nil

	case ImportMove:
		if err := MoveAtomic(src, dst); err != nil {
			return fmt.Errorf("fsops: import %s: %w", src, err)
		}
		log.Debug("fsops: imported", "src", src, "dst", dst, "mode", mode.String())
		return nil

	default:
		return fmt.Errorf("fsops: unknown import mode %d", int(mode))
	}
}
