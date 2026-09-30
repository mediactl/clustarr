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

package rescan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// scanMissingFolder finishes a scan whose path, narrowed by spec.subpath,
// does not exist under a root folder that does, and reports whether it
// did. An item added before anything was downloaded has no folder yet, so
// Rescan on it named one the walk's first lstat could not find: the task
// failed on every delivery and was dead-lettered (2026-09-30). As Sonarr's
// RescanSeries logs "Series folder doesn't exist" and carries on, the scan
// finishes with nothing found; a manual assignment, which names one file
// the person picked, is refused instead, so they learn it is gone. A root
// folder that is itself missing -- an unmounted share -- is not an empty
// library, so that scan goes on to fail as before, and so does any other
// error, which the walk reports.
func (w *Worker) scanMissingFolder(ctx context.Context, st *scanState) (bool, error) {
	root, path := filepath.Clean(st.root.Spec.Path), filepath.Clean(st.task.Path)
	if path == root {
		return false, nil
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return false, nil
	}
	rel := relPath(root, path)
	if st.manual != nil {
		return true, w.refuseScan(ctx, st, refuse("%q does not exist under root folder %q", rel, st.root.Name))
	}
	logging.FromContext(ctx).Info("library scan: the folder does not exist yet; nothing to scan",
		"rootFolder", st.root.Name, "subpath", rel)
	st.progress.Done = true
	if err := w.checkpoint(ctx, st, true); err != nil {
		return true, events.Retry(checkpointInterval, err)
	}
	return true, nil
}
