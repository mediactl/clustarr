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

package segments

import (
	"sort"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// maxSegments is FileMarkers.Segments' MaxItems.
const maxSegments = 20

// errorTTL is how long an analysis Error stands before the file is due.
const errorTTL = 24 * time.Hour

var kinds = []catalogv1alpha1.MarkerKind{
	catalogv1alpha1.MarkerIntro, catalogv1alpha1.MarkerRecap,
	catalogv1alpha1.MarkerCredits, catalogv1alpha1.MarkerPreview,
}

// Merge applies precedence per kind: TheIntroDB's segments of a kind when it
// has any, else the chapters' of that kind, else analysis's of at least 60;
// except that a credits chapter wholly titled as credits
// (ExactCreditsConfidence) outranks TheIntroDB's credits (owner's ruling,
// 2026-10-01).
// theintrodb holds TheIntroDB's segments; analysis holds the chapter and
// analysis segments, told apart by Source. The result is ordered by start
// and capped at 20.
func Merge(theintrodb, analysis []Segment) []Segment {
	var out []Segment
	for _, k := range kinds {
		switch {
		case k == catalogv1alpha1.MarkerCredits && len(of(analysis, k, catalogv1alpha1.SegmentSourceChapters, ExactCreditsConfidence)) > 0:
			out = append(out, of(analysis, k, catalogv1alpha1.SegmentSourceChapters, ExactCreditsConfidence)...)
		case has(theintrodb, k, ""):
			out = append(out, of(theintrodb, k, "", 0)...)
		case has(analysis, k, catalogv1alpha1.SegmentSourceChapters):
			out = append(out, of(analysis, k, catalogv1alpha1.SegmentSourceChapters, 0)...)
		default:
			out = append(out, of(analysis, k, catalogv1alpha1.SegmentSourceAnalysis, minConfidence)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	if len(out) > maxSegments {
		out = out[:maxSegments]
	}
	return out
}

func has(s []Segment, k catalogv1alpha1.MarkerKind, src catalogv1alpha1.SegmentSource) bool {
	return len(of(s, k, src, 0)) > 0
}

// of is s's segments of kind k (and source src, when not empty) with at
// least minConf confidence.
func of(s []Segment, k catalogv1alpha1.MarkerKind, src catalogv1alpha1.SegmentSource, minConf int32) []Segment {
	var out []Segment
	for _, x := range s {
		if x.Kind == k && (src == "" || x.Source == src) && x.Confidence >= minConf {
			out = append(out, x)
		}
	}
	return out
}

// Due reports whether mf needs analysis now: a probed movie or episode
// file never analyzed, analyzed for another probe or by an older analyzer,
// or whose analysis failed a day or more ago. A NotFound or Found at the
// current version and probe is never due again: the same bytes give the
// same answer.
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
	case a.ForProbeHash != mf.Status.ProbeHash, a.Version < AnalyzerVersion:
		return true
	case a.Result == catalogv1alpha1.MarkersError:
		return now.Sub(a.AnalyzedAt.Time) >= errorTTL
	}
	return false
}
