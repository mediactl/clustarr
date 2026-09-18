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

package rollup

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// FileState derives a catalog item's file-related status fields from the
// MediaFile currently backing it. mf is nil when none does (a fresh item, or
// one whose file was just removed) -- every return value is then the zero
// value. profile is nil when the owning QualityProfile could not be
// resolved; cutoffMet is conservatively false in that case rather than
// panicking or guessing.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool) {
	if mf == nil {
		return false, nil, nil, 0, false
	}
	name := mf.Name
	cutoffMet = profile != nil && profile.CutoffMet(mf.Spec.Quality)
	return true, &name, &mf.Spec.Quality, mf.Spec.FormatScore, cutoffMet
}
