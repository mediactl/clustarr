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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

// maxFileRefs mirrors AudiobookStatus.FileRefs'
// +kubebuilder:validation:MaxItems=200 (spec §4.2's "≤200 audio parts").
// PartRefs enforces it directly rather than relying on the apiserver to
// reject an over-long apply, because a rejected apply would also drop every
// other field this reconciler's single PatchStatus call carries.
const maxFileRefs = 200

// FileState derives an Audiobook's file-related status fields from every
// MediaFile currently backing it. items is empty for a fresh item or one
// whose files were just removed -- every return value is then the zero
// value, exactly like rollup.FileState(nil, ...).
//
// fileRefs lists every matching MediaFile's name in play order and
// partCount counts the parts on disk (PartRefs, parts.go: by each part's
// spec.partIndex, which the import sets, 2026-11-10 design D11; the
// unnumbered after the numbered, by path and then name). Design §771
// describes the import shape this orders: "N audio parts become N
// MediaFiles" under one release folder.
//
// fileQuality, fileFormatScore and cutoffMet are a single verdict, not one
// per part, because AudiobookStatus carries one of each (design D5): every
// part of one audiobook release is the same rip, so rollup.PickMediaFile's
// existing Original-preferred/most-recent selection (used verbatim, not
// reimplemented -- per the C6 controller amendment this logic lives once)
// picks one representative part and rollup.FileState scores that one
// against profile, its frozen spec.quality and spec.formatScore the ones
// written. profile is nil when the owning QualityProfile could not be
// resolved; cutoffMet is then conservatively false, never a guess.
func FileState(items []catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRefs []string, partCount int32, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool) {
	if len(items) == 0 {
		return false, nil, 0, nil, 0, false
	}
	fileRefs, partCount = PartRefs(items)
	_, _, fileQuality, fileFormatScore, cutoffMet = rollup.FileState(rollup.PickMediaFile(items), profile)
	return true, fileRefs, partCount, fileQuality, fileFormatScore, cutoffMet
}
