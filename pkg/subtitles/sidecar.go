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
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/mediactl/clustarr/pkg/fsops"
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

// Write atomically replaces the file at path with content, creating it
// with mode: it delegates to fsops.AtomicWrite, which writes a .partial
// file beside the destination, fsyncs both that file and the directory,
// then renames it into place. A reader never observes a partial subtitle
// and a crash never leaves a truncated one.
//
// mode is the caller's (RootFolderSpec.Perms.FileMode, "0664" by
// default): sidecars sit beside the video in a group-shared library, so a
// media server running as another uid in the same group must be able to
// read them. The process umask still applies; the deployment sets
// UMASK 002 (spec section 11).
func (Writer) Write(ctx context.Context, path string, content []byte, mode os.FileMode) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := fsops.AtomicWrite(path, bytes.NewReader(content), mode); err != nil {
		return fmt.Errorf("subtitles: write sidecar %s: %w", path, err)
	}
	return nil
}
