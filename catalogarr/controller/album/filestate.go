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

// FileState derives an Album's file-related status fields from every
// MediaFile backing it (spec.mediaRef {kind: album, name: <this Album>},
// with or without a track): AlbumStatus.Quality is "the lowest quality
// across the album's imported tracks", and CutoffMet is "true when the
// imported tracks meet the profile cutoff". This is Lidarr's rule
// (develop da7b4dfb): CutoffSpecification collects every track file's
// quality (`trackFiles.Select(c => c.Quality).Distinct()`), and
// UpgradableSpecification.CutoffNotMet reports the cutoff unmet as soon as
// ANY one of them is below it -- so one lossy track in a lossless album
// keeps the album wanted, where picking a single representative file let
// that track hide behind a better one.
//
//   - hasFile is whether any file backs the album; with none, every return
//     value is the zero value, as rollup.FileState(nil, ...) gives.
//   - cutoffMet is true only when every file meets the cutoff: a transcoded
//     file always does (rollup.Transcoded, the rule rollup.FileState applies
//     to one file), any other file when profile ranks its quality at or
//     above the cutoff tier. An unresolved profile (nil) meets nothing, as
//     rollup.FileState's does.
//   - fileQuality is the lowest-ranked file's quality under profile: the
//     greatest tier index, and a quality the profile does not list below
//     every quality it does (profile.CutoffMet treats it as unmet too).
//     Files tied at the lowest rank are chosen between by
//     rollup.PickMediaFile, so the answer never depends on list order.
//     Without a profile there is no ranking to find a lowest by, so it is
//     the quality of the file rollup.PickMediaFile selects.
//   - formatScore is the lowest file's custom-format score, by the same
//     "every file must measure up" reading. A music profile scores no custom
//     formats (quality.FromCRD), so for a real album it is 0.
//
// Deviation: Lidarr also counts the cutoff unmet while any track of the
// monitored release has no file (TracksWithoutFiles). Clustarr cannot tell
// which tracks a file holds unless the importer attributes it
// (commonv1.MediaRef.Track, today only a one-track album's lone file), so
// that clause would hold every multi-track album below the cutoff forever;
// it is left out until track attribution exists.
func FileState(mfs []catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileQuality *commonv1.Quality, formatScore int32, cutoffMet bool) {
	files := make([]catalogv1alpha1.MediaFile, 0, len(mfs))
	for _, mf := range mfs {
		if mf.Spec.MediaRef.Kind == commonv1.MediaKindAlbum {
			files = append(files, mf)
		}
	}
	if len(files) == 0 {
		return false, nil, 0, false
	}

	cutoffMet = true
	formatScore = files[0].Spec.FormatScore
	for i := range files {
		mf := &files[i]
		if !rollup.Transcoded(mf) && (profile == nil || !profile.CutoffMet(mf.Spec.Quality)) {
			cutoffMet = false
		}
		formatScore = min(formatScore, mf.Spec.FormatScore)
	}

	lowest := rollup.PickMediaFile(files)
	if profile != nil {
		lowest = rollup.PickMediaFile(lowestRanked(files, *profile))
	}
	q := lowest.Spec.Quality
	return true, &q, formatScore, cutoffMet
}

// lowestRanked returns the files whose quality ranks lowest under profile:
// the greatest tier index, with a quality the profile does not list ranking
// below all of them. files is non-empty.
func lowestRanked(files []catalogv1alpha1.MediaFile, profile quality.Profile) []catalogv1alpha1.MediaFile {
	rank := func(q commonv1.Quality) int {
		if idx, ok := profile.Index(q); ok {
			return idx
		}
		return len(profile.Tiers) // below every tier the profile lists
	}
	worst := -1
	var out []catalogv1alpha1.MediaFile
	for _, mf := range files {
		switch r := rank(mf.Spec.Quality); {
		case r > worst:
			worst, out = r, []catalogv1alpha1.MediaFile{mf}
		case r == worst:
			out = append(out, mf)
		}
	}
	return out
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
