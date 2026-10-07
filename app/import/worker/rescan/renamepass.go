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
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxRenameReason is catalogv1alpha1.RenamedFile.Reason's MaxLength, in
// characters: an error recorded as a reason is clamped to it, or the
// LibraryScan controller's status apply would be rejected whole.
const maxRenameReason = 256

// renameMode is the scan's rename pass: whether it runs, and whether it is a
// dry run. spec.rename "" is off, like the CRD's default. A scan that is
// itself a dry run (spec.dryRun) creates and updates nothing, so its rename
// pass is a dry run whatever spec.rename says.
func (s *scanState) renameMode() (run, dryRun bool) {
	switch s.scan.Spec.Rename {
	case catalogv1alpha1.ScanRenameDryRun:
		return true, true
	case catalogv1alpha1.ScanRenameApply:
		return true, s.task.DryRun
	}
	return false, false
}

// renamePass is a LibraryScan's rename pass (probe-driven naming spec §5).
// It runs after the walk, so the scan's own observations land first, and
// hands every rename candidate ([mediafilespec.Renameable]) whose spec.path is under the
// walked path to [mediafilespec.RenameFile], in path order, recording each outcome in the
// tally's Renamed list ([scanprogress.MergeRenamed]) and each move in FilesRenamed.
//
// This pass is the scan the rename controller waits for (ruling R25), so it
// calls RenameFile directly. A [mediafilespec.RenameNotCurrent] outcome is not recorded:
// there is nothing to do -- the uncached read found no proposal, or the
// file already at it, because the cached status was behind or an earlier
// delivery of this task moved the file. An error renaming one file is
// logged and recorded as "Failed: <error>" ([mediafilespec.RenameFailed]), and the pass
// carries on; only a failure to list the candidates, or to extend the
// delivery's deadline, ends it. The deadline is extended before each file,
// as the walk extends it.
func (w *Worker) renamePass(ctx context.Context, m events.Message, st *scanState) error {
	run, dryRun := st.renameMode()
	if !run {
		return nil
	}
	candidates, err := w.renameCandidates(ctx, st)
	if err != nil {
		return err
	}
	api := w.APIReader
	if api == nil {
		api = w.Client
	}
	log := logging.FromContext(ctx)
	for i := range candidates {
		if err := w.beat(ctx, m, st); err != nil {
			return err
		}
		mf := &candidates[i]
		out, err := mediafilespec.RenameFile(ctx, w.Client, api, mf, dryRun, false)
		entry := scanprogress.RenamedFile{From: out.From, To: out.To, Reason: out.Reason}
		switch {
		case err != nil:
			log.Warn("the rename pass could not rename a file; carrying on", "mediaFile", mf.Name, "error", err)
			entry.Reason = clampRunes(mediafilespec.RenameFailed+": "+err.Error(), maxRenameReason)
		case out.Reason == mediafilespec.RenameNotCurrent:
			continue
		case out.Moved:
			st.progress.FilesRenamed++
		}
		if entry.From == "" {
			entry.From = mf.Spec.Path
		}
		if entry.To == "" && mf.Status.Naming != nil {
			entry.To = mf.Status.Naming.ExpectedPath
		}
		st.progress.Renamed = scanprogress.MergeRenamed(st.progress.Renamed, entry)
		if err := w.checkpoint(ctx, st, false); err != nil {
			return err
		}
	}
	log.Info("rename pass finished", "dryRun", dryRun, "candidates", len(candidates),
		"filesRenamed", st.progress.FilesRenamed)
	return nil
}

// renameCandidates lists, from the cache, the scan namespace's
// [mediafilespec.Renameable] MediaFiles whose spec.path is the walked path or beneath it,
// sorted by path. The spec.path index is exact-match, so the namespace's
// MediaFiles are listed and filtered on a path-separator boundary: a scan
// of "Heat" must not take "Heat 2".
func (w *Worker) renameCandidates(ctx context.Context, st *scanState) ([]catalogv1alpha1.MediaFile, error) {
	var list catalogv1alpha1.MediaFileList
	if err := w.Client.List(ctx, &list, client.InNamespace(st.scan.Namespace)); err != nil {
		return nil, fmt.Errorf("rescan: list media files for the rename pass: %w", err)
	}
	walked := filepath.Clean(st.task.Path)
	out := list.Items[:0]
	for i := range list.Items {
		mf := &list.Items[i]
		p := filepath.Clean(mf.Spec.Path)
		if k8s.IsDeleting(mf) || !mediafilespec.Renameable(mf) ||
			(p != walked && !strings.HasPrefix(p, walked+string(filepath.Separator))) {
			continue
		}
		out = append(out, *mf)
	}
	slices.SortFunc(out, func(a, b catalogv1alpha1.MediaFile) int { return strings.Compare(a.Spec.Path, b.Spec.Path) })
	return out, nil
}

// clampRunes cuts s to at most n characters, never splitting one: a CRD's
// MaxLength counts characters, not bytes.
func clampRunes(s string, n int) string {
	if len(s) <= n { // at most n bytes is at most n characters
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
