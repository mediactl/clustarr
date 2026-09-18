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

package subtitles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Writer writes already-normalised subtitle content to an already-resolved
// path atomically (temp file + rename in the same directory). It does not
// compute the destination path itself: sidecar naming (SidecarName) and
// parsing (ParseSidecar) — spec §7's SidecarName(videoPath string, key
// LangKey, hiExt string) string and ParseSidecar(videoStem, name string)
// (LangKey, bool) — along with the missing-subtitle planner (Plan), are
// deferred to Phase F (the captionarr controller), per this task's Scope
// note and the controller amendment. Until then, a caller resolves the
// path itself and passes it straight to Write.
type Writer struct{}

func NewWriter() Writer { return Writer{} }

// Write atomically replaces the file at path with content: it writes to a
// temp file in the same directory, then renames it into place, so a reader
// never observes a partially-written subtitle file.
func (Writer) Write(ctx context.Context, path string, content []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".subtitles-*.tmp")
	if err != nil {
		return fmt.Errorf("subtitles: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("subtitles: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("subtitles: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("subtitles: rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
