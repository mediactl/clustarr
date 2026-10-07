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

// Package mediafile is the MediaFile domain the remediation loop calls
// (ADR-0016): the probe's decision (PlanProbe, judgeProbe), naming
// (LoadNaming, RenderNaming), the labels catalogarr mirrors, and the indexes
// and map functions the loop's sources read. It holds no controller.
package mediafile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// AnnotationObservedFingerprint is how importarr's library rescan tells the
// remediation loop that a post-transcode file changed on disk: "<sizeBytes>@<RFC
// 3339 mtime>" as the walk found it, applied under k8s.ManagerImportarr,
// which owns nothing else on a MediaFile. It is the same key as
// app/import/worker/rescan.AnnotationObservedFingerprint (a test holds the
// two equal; catalogarr does not import importarr to read one constant).
//
// This is the gap-fix X5a/X7a contract for "a transcoded file's bytes
// legitimately changed". Once catalogarr incorporates a transcode swap it
// owns spec.sizeBytes, spec.modTime and spec.original, so the rescan never
// re-applies them -- k8s.Apply forces ownership and would silently take
// them back. The rescan only OBSERVES and hands over through this
// annotation; the loop ACTS: the annotation's change wakes the file (a
// metadata change bumps no generation, so S1 watches the annotation, loop
// spec §3.3), and the pass re-stats the file, re-probes it when its
// fingerprint moved, re-records size and mtime under catalogarr and drops
// the transcode verdict the old bytes earned. The value is never
// parsed: the stat is the authority, the annotation only the doorbell.
const AnnotationObservedFingerprint = "catalog.clustarr.io/observed-fingerprint"

// TranscodedRecheckInterval is how often a transcoded MediaFile
// (spec.original=false) is re-examined on disk when nothing else wakes it:
// the floor under [AnnotationObservedFingerprint], for a library whose root
// folder has no rescan schedule, or a change between two scans. A day is the
// budget: one stat, two cached Lists and one no-op apply per transcoded file
// per day.
const TranscodedRecheckInterval = 24 * time.Hour

// SwapTarget decides what an unincorporated Succeeded TranscodeJob means for
// mf, from where the job put its output (status.result.outputPath; empty on
// a job that predates the field, which always wrote in place) and whether
// the source is still there -- the one fact squasharr's worker settles
// before it records Succeeded (gap-fix R-11, x10-report.md):
//
//   - output at spec.path: the in-place swap of spec §8.5. path is
//     spec.path.
//   - output under a new name, source gone: replaceSource=true with a
//     container change or an explicit spec.outputPath. The worker places
//     the output, then retires the source, then reports Succeeded. The swap
//     is incorporated at the new name: path is the output, and the caller
//     takes spec.path over with it.
//   - output under a new name, source still present: replaceSource=false.
//     The encode is a derived copy beside the kept source, which stays this
//     MediaFile's file. kept is the job; path is spec.path.
//
// The kept copy is deliberately referenced by nothing on the MediaFile: the
// Succeeded TranscodeJob already records it (spec.mediaFileRef names this
// MediaFile, status.result.outputPath the copy), and a second MediaFile for
// it would back the same item twice -- PickMediaFile would choose between
// them, and squasharr and captionarr would treat the copy as a new library
// file to transcode and subtitle. It is Jellyfin's "<name> - <label>"
// multiple-version convention (docs/research/naming.md §A3): the media
// server shows it as a version of the same item, and the catalog does not
// need to.
//
// Once a kept job is recorded, the probe that records it moves
// status.probedAt past the job's finishedAt, so it is never reconsidered:
// deleting the kept source later reads as FileMissing, not as licence to
// adopt the copy.
//
// The source's presence is stat'ed through stat -- the remediation loop's
// /data I/O executor (loop spec §3.17), so a hung mount never pins a loop
// worker -- and a stat that failed for any reason but the file being absent
// is returned as err, never read as "the source is gone".
func SwapTarget(mf *catalogv1alpha1.MediaFile, swap *transcodev1alpha1.TranscodeJob,
	stat func(string) (fs.FileInfo, error),
) (path string, kept *transcodev1alpha1.TranscodeJob, err error) {
	path = mf.Spec.Path
	if swap == nil || swap.Status.Result == nil {
		return path, nil, nil
	}
	out := swap.Status.Result.OutputPath
	if out == "" || out == mf.Spec.Path {
		return path, nil, nil
	}
	switch _, err := stat(mf.Spec.Path); {
	case err == nil:
		return path, swap, nil
	case errors.Is(err, fs.ErrNotExist):
		return out, nil, nil
	default:
		return path, nil, err
	}
}

// staleTranscodeState is t with the compliance verdict dropped: Compliant
// false and no ProfileTag, because the bytes they described are gone.
// LastResult and JobRef are kept -- they are about the last transcode, not
// about the file as it now is. A nil t stays nil.
func staleTranscodeState(t *catalogv1alpha1.TranscodeState) *catalogv1alpha1.TranscodeState {
	if t == nil {
		return nil
	}
	out := *t
	out.Compliant = false
	out.ProfileTag = ""
	return &out
}

// RegisterIndexes registers every field index the remediation loop's
// lookups and map functions read through this package, on idx, under their
// unchanged names: app/remediation.RegisterIndexes calls it, so the index
// names and extractors live in one place.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &transcodev1alpha1.AudioGraft{}, audioGraftMediaFileRefIndex, indexAudioGraftByMediaFileRef); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &transcodev1alpha1.TranscodeJob{}, transcodeJobMediaFileRefIndex, indexTranscodeJobByMediaFileRef); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &subtitlev1alpha1.SubtitleRequest{}, subtitleRequestMediaFileRefIndex, indexSubtitleRequestByMediaFileRef); err != nil {
		return err
	}
	return registerNamingIndexes(ctx, idx)
}

// TranscodeJobsOf lists mf's TranscodeJobs through the spec.mediaFileRef
// index, reading through c (the manager's cache).
func TranscodeJobsOf(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) ([]transcodev1alpha1.TranscodeJob, error) {
	var list transcodev1alpha1.TranscodeJobList
	if err := c.List(ctx, &list, client.InNamespace(mf.Namespace),
		client.MatchingFields{transcodeJobMediaFileRefIndex: mf.Name}); err != nil {
		return nil, fmt.Errorf("mediafile: list TranscodeJobs: %w", err)
	}
	return list.Items, nil
}

// LatestUnincorporatedTranscode returns the most recently finished
// Succeeded job in jobs whose FinishedAt is after probedAt (every Succeeded
// one when probedAt is nil), or nil if none. This is what tells the probe
// planner "a transcode swap happened and it is not folded in yet" without
// needing to know which watch woke the file.
func LatestUnincorporatedTranscode(jobs []transcodev1alpha1.TranscodeJob, probedAt *metav1.Time) *transcodev1alpha1.TranscodeJob {
	var latest *transcodev1alpha1.TranscodeJob
	for i := range jobs {
		tj := &jobs[i]
		if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded || tj.Status.FinishedAt == nil {
			continue
		}
		if probedAt != nil && !tj.Status.FinishedAt.After(probedAt.Time) {
			continue
		}
		if latest == nil || tj.Status.FinishedAt.After(latest.Status.FinishedAt.Time) {
			latest = tj
		}
	}
	return latest
}

// TranscodeInFlight reports whether any of jobs has not reached a terminal
// phase (Succeeded, Failed or Skipped) -- including one squasharr has not
// yet given a phase at all. Such a job holds the file: its worker re-probes
// spec.sourcePath and swaps its output over it, so a rename underneath it
// would fail the encode or strand the output under the old name (spec D6).
func TranscodeInFlight(jobs []transcodev1alpha1.TranscodeJob) bool {
	for i := range jobs {
		switch jobs[i].Status.Phase {
		case transcodev1alpha1.TranscodeJobPhaseSucceeded,
			transcodev1alpha1.TranscodeJobPhaseFailed,
			transcodev1alpha1.TranscodeJobPhaseSkipped:
		default:
			return true
		}
	}
	return false
}

// ProfileTag renders §4.5's "<profile>@<hash>", the CLUSTARR_PROFILE value
// a transcode under profile at revision hash carries.
func ProfileTag(profile, hash string) string { return profile + "@" + hash }
