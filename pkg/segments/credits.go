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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Credit bounds and agreement (spec §6.3).
const (
	minCreditsMs      = 15_000
	maxEpisodeCredits = 450_000
	maxMovieCredits   = 900_000
	endSlackMs        = 5_000 // credits end within this of the file's end, or of a preview
	agreeMs           = 5_000 // two signals whose starts are this close agree
	agreedConfidence  = 90
	standConfidence   = 70 // a single signal stands at or above this
	minConfidence     = 60 // below this nothing is written
)

// Credits decides a file's credits from candidates (chapters at 100, an
// ending theme, frame runs, the DNN). A chapter wins outright; two others
// starting within 5 s agree at 90, from the earlier start; otherwise the
// most confident single one stands at 70 or above. Credits run 15 s to 450 s
// (900 s for a movie) and end within 5 s of the file's end, or of
// previewStartMs when a preview follows them (0 when none does).
func Credits(durationMs int64, movie bool, cands []Segment, previewStartMs int64) (Segment, bool) {
	maxMs := int64(maxEpisodeCredits)
	if movie {
		maxMs = maxMovieCredits
	}
	var valid []Segment
	for _, c := range cands {
		n := c.EndMs - c.StartMs
		endsRight := durationMs-c.EndMs <= endSlackMs ||
			(previewStartMs > 0 && abs64(previewStartMs-c.EndMs) <= endSlackMs)
		if c.Kind == catalogv1alpha1.MarkerCredits && n >= minCreditsMs && n <= maxMs && endsRight {
			valid = append(valid, c)
		}
	}
	for _, c := range valid {
		if c.Source == catalogv1alpha1.SegmentSourceChapters {
			return c, true
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].StartMs < valid[j].StartMs })
	for i := range valid {
		for j := i + 1; j < len(valid); j++ {
			if valid[j].StartMs-valid[i].StartMs <= agreeMs {
				c := valid[i]
				c.EndMs = max(c.EndMs, valid[j].EndMs)
				c.Confidence = agreedConfidence
				return c, true
			}
		}
	}
	best := -1
	for i, c := range valid {
		if c.Confidence >= standConfidence && (best < 0 || c.Confidence > valid[best].Confidence) {
			best = i
		}
	}
	if best < 0 {
		return Segment{}, false
	}
	return valid[best], true
}

// AnimePreview is the preview an anime episode carries after its credits:
// from their end to the file's end, when that is more than 5 s.
func AnimePreview(credits Segment, durationMs int64) (Segment, bool) {
	if durationMs-credits.EndMs <= endSlackMs {
		return Segment{}, false
	}
	return Segment{
		Kind: catalogv1alpha1.MarkerPreview, StartMs: credits.EndMs, EndMs: durationMs,
		Source: catalogv1alpha1.SegmentSourceAnalysis, Confidence: standConfidence,
	}, true
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
