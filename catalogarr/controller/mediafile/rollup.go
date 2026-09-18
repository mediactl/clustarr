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

package mediafile

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// rollupInput is the MediaFile-derived half of what an owning Movie or
// Episode's status rollup needs; the other half (which item, which
// condition helper) is the reconciler's, not this pure function's.
type rollupInput struct {
	FileRef     string
	Quality     commonv1.Quality
	FormatScore int32
	Profile     quality.Profile
	HasProfile  bool // false when the item's QualityProfileRef did not resolve
}

type rollupResult struct {
	HasFile         bool
	FileRef         string
	FileQuality     commonv1.Quality
	FileFormatScore int32
	CutoffMet       bool
}

// computeRollup mirrors a MediaFile onto the four fields spec §4.2 lists as
// MediaFile-derived on Movie/Episode. An unresolved profile reports
// CutoffMet=false rather than guessing -- amendment §A1.5's never-guess rule
// extended to a missing reference, not just an unmatched file.
func computeRollup(in rollupInput) rollupResult {
	cutoffMet := false
	if in.HasProfile {
		cutoffMet = in.Profile.CutoffMet(in.Quality)
	}
	return rollupResult{
		HasFile:         true,
		FileRef:         in.FileRef,
		FileQuality:     in.Quality,
		FileFormatScore: in.FormatScore,
		CutoffMet:       cutoffMet,
	}
}

// moviePhaseForFile and episodePhaseForFile pick the terminal phase once a
// file exists; the reconciler only calls these on the has-a-file edge, never
// overwriting Pending/Unavailable/Wanted/Delayed/Downloading phases that
// belong to the item's own controller.
func moviePhaseForFile(cutoffMet bool) catalogv1alpha1.MoviePhase {
	if cutoffMet {
		return catalogv1alpha1.MoviePhaseImported
	}
	return catalogv1alpha1.MoviePhaseCutoffUnmet
}

func episodePhaseForFile(cutoffMet bool) catalogv1alpha1.EpisodePhase {
	if cutoffMet {
		return catalogv1alpha1.EpisodePhaseImported
	}
	return catalogv1alpha1.EpisodePhaseCutoffUnmet
}
