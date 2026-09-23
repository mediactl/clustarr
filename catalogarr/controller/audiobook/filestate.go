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

package audiobook

import (
	"sort"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

// maxFileRefs mirrors AudiobookStatus.FileRefs'
// +kubebuilder:validation:MaxItems=200 (spec §4.2's "≤200 audio parts").
// FileState enforces it directly rather than relying on the apiserver to
// reject an over-long apply, because a rejected apply would also drop every
// other field this reconciler's single PatchStatus call carries.
const maxFileRefs = 200

// FileState derives an Audiobook's file-related status fields from every
// MediaFile currently backing it. items is empty for a fresh item or one
// whose files were just removed -- every return value is then the zero
// value, exactly like rollup.FileState(nil, ...).
//
// fileRefs lists every matching MediaFile's name, in "play order" (status
// §4.2's phrase). Design §771 describes the import shape this orders: "N
// audio parts become N MediaFiles" under one release folder. Neither
// MediaFileSpec nor the design carries an explicit part index for this case
// (pkg/naming's Context.PartNumber exists for a future per-file naming
// token, but nothing sets it yet), so the only stable, available-today
// signal is the file's own spec.path -- and a naming layout that numbers its
// parts ("... 01.mp3", "... 02.mp3", per pkg/naming's audiobookFileTemplate
// family) sorts correctly under a plain lexical compare. This is a
// documented judgment call, not a spec-mandated ordering; a future importer
// task that adds a real part index should sort on that instead. Name is a
// tie-breaker only, for two files that somehow share a path (never expected
// in practice: MediaFile is one object per real file).
//
// quality and cutoffMet are a single verdict, not one per part, because
// AudiobookStatus.Quality is a single field (spec §4.2): every part of one
// audiobook release is the same rip, so rollup.PickMediaFile's existing
// Original-preferred/most-recent selection (used verbatim, not
// reimplemented -- per the C6 controller amendment this logic lives once)
// picks one representative part and rollup.FileState scores that one
// against profile. profile is nil when the owning QualityProfile could not
// be resolved; cutoffMet is then conservatively false, never a guess.
func FileState(items []catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRefs []string, q *commonv1.Quality, cutoffMet bool) {
	if len(items) == 0 {
		return false, nil, nil, false
	}

	sorted := make([]catalogv1alpha1.MediaFile, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Spec.Path != sorted[j].Spec.Path {
			return sorted[i].Spec.Path < sorted[j].Spec.Path
		}
		return sorted[i].Name < sorted[j].Name
	})

	refs := make([]string, 0, len(sorted))
	for i := range sorted {
		refs = append(refs, sorted[i].Name)
	}
	if len(refs) > maxFileRefs {
		refs = refs[:maxFileRefs]
	}

	_, _, fq, _, met := rollup.FileState(rollup.PickMediaFile(items), profile)
	return true, refs, fq, met
}
