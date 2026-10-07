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

import "sort"

// maxSegments is FileMarkers.Segments' MaxItems.
const maxSegments = 20

var kinds = []Kind{
	KindIntro, KindRecap,
	KindCredits, KindPreview,
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
		case k == KindCredits && len(of(analysis, k, SourceChapters, ExactCreditsConfidence)) > 0:
			out = append(out, of(analysis, k, SourceChapters, ExactCreditsConfidence)...)
		case has(theintrodb, k, ""):
			out = append(out, of(theintrodb, k, "", 0)...)
		case has(analysis, k, SourceChapters):
			out = append(out, of(analysis, k, SourceChapters, 0)...)
		default:
			out = append(out, of(analysis, k, SourceAnalysis, minConfidence)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	if len(out) > maxSegments {
		out = out[:maxSegments]
	}
	return out
}

func has(s []Segment, k Kind, src Source) bool {
	return len(of(s, k, src, 0)) > 0
}

// of is s's segments of kind k (and source src, when not empty) with at
// least minConf confidence.
func of(s []Segment, k Kind, src Source, minConf int32) []Segment {
	var out []Segment
	for _, x := range s {
		if x.Kind == k && (src == "" || x.Source == src) && x.Confidence >= minConf {
			out = append(out, x)
		}
	}
	return out
}
