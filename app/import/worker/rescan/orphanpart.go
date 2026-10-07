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
	"os"
	"time"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// OrphanPartAge is how long a transcode attempt's part file must have gone
// unwritten before the rescan removes it as abandoned: the worker's default
// per-task deadline (app/transcode/jobspec.DefaultActiveDeadline, 48h, the
// TranscodeProfile CRD default) plus a day. A running encode writes its
// part continuously, so its modification time stays fresh however long a
// profile's own deadline is; one this old belongs to an attempt that died
// without its cleanup -- OOM-killed, or lost with its node. The liveness
// check below is the first guard, this age the second: a part whose job is
// still non-terminal stays at any age, since that job's next attempt sweeps
// it itself (app/transcode/worker's sweepEarlierAttempts).
const OrphanPartAge = 72 * time.Hour

// orphanPart reports whether the walked file at path is an abandoned
// transcode attempt's output: a regular file -- never a symlink, which is
// not followed -- of exactly the per-attempt form
// (fsops.ParseTranscodePart; a torrent's .part, the generic
// <stem>.part.<ext> and a release named like a part are not), unwritten for
// OrphanPartAge, whose job (by the first eight characters of its UID) is
// not among live, the scan namespace's non-terminal TranscodeJobs.
func orphanPart(path string, info os.FileInfo, now time.Time, live map[string]bool) bool {
	p, ok := fsops.ParseTranscodePart(path)
	return ok && info.Mode().IsRegular() && now.Sub(info.ModTime()) > OrphanPartAge && !live[p.JobUID8]
}

// sweepOrphanPart removes the walked part file at path when it is an
// abandoned transcode attempt's (orphanPart), and logs it. A dry run
// removes nothing. Best effort: a part that cannot be removed is logged and
// the walk goes on. The file is examined again immediately before the
// removal, so one rewritten or replaced since the walk listed it stays.
func (w *Worker) sweepOrphanPart(ctx context.Context, st *scanState, path string, info os.FileInfo) {
	now := w.now()
	if st.task.DryRun || !orphanPart(path, info, now, st.liveTranscodeJobs) {
		return
	}
	log := logging.FromContext(ctx)
	rel := relPath(st.root.Spec.Path, path)
	again, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, again) || !orphanPart(path, again, now, st.liveTranscodeJobs) {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.WarnContext(ctx, "rescan: removing an abandoned transcode part file failed", "path", rel, "error", err)
		return
	}
	log.InfoContext(ctx, "rescan: removed an abandoned transcode part file",
		"path", rel, "modified", again.ModTime().UTC(), "minAge", OrphanPartAge)
}
