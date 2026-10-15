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

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The local stage. An attempt encodes, writes its sidecars and verifies in
// the pod's local scratch ([Options.ScratchDir], the pool's emptyDir), and
// only the verified result crosses to the data volume, once, to its part's
// own name beside the output; the swap is then the rename on one filesystem
// it always was, so a crash between any two steps leaves what the package
// doc says. Over a shared NAS the encode's writes and the MP4 faststart
// rewrite cost the NAS the output twice more (owner, 2026-10-15). A pod that
// dies takes its local part with it; the next attempt encodes again, as a
// half-written part beside the output always made it.

// localStageHeadroom is the room beyond 1.5 times the source an output is
// budgeted in scratch: a remux adds the AAC companion and a surround track
// (13th: 1.51 GB to 1.83), and the sidecars sit beside it.
const localStageHeadroom = 1 << 30

// scratchPart is where this attempt encodes in local scratch: staged's name
// there, when the output fits both the profile's spec.scratch -- the
// emptyDir's size limit, past which the kubelet evicts the pod -- and the
// scratch filesystem's free space. "" encodes at staged, beside the output.
func (r *runner) scratchPart(staged string, sourceSize int64) string {
	if r.o.ScratchDir == "" {
		return ""
	}
	need := sourceSize + sourceSize/2 + localStageHeadroom
	limit := jobspec.Scratch(r.t.Profile.Spec)
	if need > limit.Value() {
		return ""
	}
	if free, err := fsops.FreeBytes(r.o.ScratchDir); err != nil || free < need {
		return ""
	}
	return filepath.Join(r.o.ScratchDir, filepath.Base(staged))
}

// stageOnData moves a verified local part, and each sidecar the plan wrote
// beside it, to staged, the part's own name beside the output on the data
// volume. Each lands whole or not at all (fsops.MoveAtomic copies across
// filesystems under a .partial name and renames).
func stageOnData(_ context.Context, plan standard.Result, local, staged string) error {
	if err := fsops.MoveAtomic(local, staged); err != nil {
		return err
	}
	for _, s := range plan.Sidecars {
		from := fsops.SidecarPath(local, s.Suffix)
		if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := fsops.MoveAtomic(from, fsops.SidecarPath(staged, s.Suffix)); err != nil {
			return err
		}
	}
	return nil
}
