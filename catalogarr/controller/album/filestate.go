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

package album

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

// FileState is rollup.FileState, re-exported so the reconciler and this
// package's own tests read as album.FileState(...), matching the movie and
// episode packages' identical re-export.
//
// Album has no per-track file attribution yet (design §9's "import matches
// tracks by number+duration via id3v2/dhowden tags" is importarr's job,
// M6/G2-4, not built) -- there is no MediaRef shape that could point at one
// specific Track within an Album (commonv1.MediaRef carries Kind+Name only;
// see this reconciler's own doc comment). So mf here is deliberately the
// single MediaFile rollup.PickMediaFile selects across every MediaFile
// referencing this Album as a whole (kind=album, name=<this Album>) -- a
// coarse, Movie-like "does at least one file back this album" signal, not a
// per-track one. status.trackFileCount is computed separately, from
// status.tracks' own FileRef field (always empty today, for the same
// reason), not from this function's result -- see rollup.go.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool) {
	return rollup.FileState(mf, profile)
}
