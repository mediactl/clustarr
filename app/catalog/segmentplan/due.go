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

package segmentplan

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/segments"
)

// analysisErrorTTL is how long an analysis Error stands before the file is
// due again.
const analysisErrorTTL = 24 * time.Hour

// Due reports whether mf wants a segment analysis: a movie or episode file
// with a probe, never analyzed, analyzed for another probe or by an older
// analyzer, or whose analysis failed a day or more ago. A NotFound or Found
// at the current version and probe is never due again: the same bytes give
// the same answer. (Moved from pkg/segments, which no longer links the API.)
func Due(mf *catalogv1alpha1.MediaFile, now time.Time) bool {
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
	default:
		return false
	}
	if mf.Status.MediaInfo == nil || mf.Status.ProbeHash == "" {
		return false
	}
	if mf.Status.Markers == nil || mf.Status.Markers.Analysis == nil {
		return true
	}
	a := mf.Status.Markers.Analysis
	switch {
	case a.ForProbeHash != mf.Status.ProbeHash, a.Version < segments.AnalyzerVersion:
		return true
	case a.Result == catalogv1alpha1.MarkersError:
		return now.Sub(a.AnalyzedAt.Time) >= analysisErrorTTL
	}
	return false
}
