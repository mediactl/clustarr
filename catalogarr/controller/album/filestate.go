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
// mf is the single MediaFile rollup.PickMediaFile selects across every
// MediaFile referencing this Album (kind=album, name=<this Album>), with or
// without a track: a coarse, Movie-like "does a file back this album"
// signal that phase, quality and cutoff are decided from. The per-track
// picture is FilesByRecording's: status.tracks[].fileRef and
// status.trackFileCount come from it.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool) {
	return rollup.FileState(mf, profile)
}

// FilesByRecording maps each recording MBID to the MediaFile holding it,
// from the MediaFiles that address a single track of this Album
// (spec.mediaRef {kind: album, name: <album>, track: <recording MBID>},
// commonv1.MediaRef.Track). A file that addresses the whole album carries no
// track and is not in the map. When two files claim one recording --
// an upgrade whose importer has not yet removed the file it replaces --
// rollup.PickMediaFile chooses between them, so the answer never depends on
// list order.
func FilesByRecording(mfs []catalogv1alpha1.MediaFile) map[string]string {
	byRecording := map[string][]catalogv1alpha1.MediaFile{}
	for _, mf := range mfs {
		if mf.Spec.MediaRef.Kind != commonv1.MediaKindAlbum || mf.Spec.MediaRef.Track == "" {
			continue
		}
		byRecording[mf.Spec.MediaRef.Track] = append(byRecording[mf.Spec.MediaRef.Track], mf)
	}
	files := make(map[string]string, len(byRecording))
	for recording, claims := range byRecording {
		if mf := rollup.PickMediaFile(claims); mf != nil {
			files[recording] = mf.Name
		}
	}
	return files
}
