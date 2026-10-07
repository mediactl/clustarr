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
// (LoadNaming, RenderNaming), the labels catalog mirrors, and the indexes
// and map functions the loop's sources read. It holds no controller.
package mediafile

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// AnnotationObservedFingerprint is how import's library rescan tells the
// remediation loop that a post-transcode file changed on disk: "<sizeBytes>@<RFC
// 3339 mtime>" as the walk found it, applied under k8s.ManagerImport,
// which owns nothing else on a MediaFile. It is the same key as
// app/import/worker/rescan.AnnotationObservedFingerprint (a test holds the
// two equal; the catalog domain does not import the import domain to read
// one constant).
//
// This is the gap-fix X5a/X7a contract for "a transcoded file's bytes
// legitimately changed". Once catalog incorporates a transcode swap it
// owns spec.sizeBytes, spec.modTime and spec.original, so the rescan never
// re-applies them -- k8s.Apply forces ownership and would silently take
// them back. The rescan only OBSERVES and hands over through this
// annotation; the loop ACTS: the annotation's change wakes the file (a
// metadata change bumps no generation, so S1 watches the annotation, loop
// spec §3.3), and the pass re-stats the file, re-probes it when its
// fingerprint moved, re-records size and mtime under catalog and drops
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

// SwapTarget is the path a Swapping transcode block's output is probed at
// (loop spec §5.9 "The swap"): result.outputPath when it is set and differs
// from spec.path -- replaceSource=true with a container change (every MP4
// output of an .mkv): the worker placed the output, then retired the
// source, and the swap is incorporated at the new name, the caller taking
// spec.path over with it -- else spec.path, an in-place swap. A
// replaceSource=false copy never reaches Swapping (the transcode planner
// records it Succeeded at once, §5.14), so the probe planner never meets
// one.
func SwapTarget(mf *catalogv1alpha1.MediaFile, t *catalogv1alpha1.TranscodeState) string {
	if t == nil || t.Result == nil || t.Result.OutputPath == "" || t.Result.OutputPath == mf.Spec.Path {
		return mf.Spec.Path
	}
	return t.Result.OutputPath
}

// RegisterIndexes registers every field index the remediation loop's
// lookups and map functions read through this package, on idx, under their
// unchanged names: app/remediation.RegisterIndexes calls it, so the index
// names and extractors live in one place.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &transcodev1alpha1.AudioGraft{}, AudioGraftMediaFileRefIndex, indexAudioGraftByMediaFileRef); err != nil {
		return err
	}
	return registerNamingIndexes(ctx, idx)
}
