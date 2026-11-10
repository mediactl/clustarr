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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

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
// 2026-10-01), and TheIntroDB's credits that cannot be the closing credits
// (implausibleCredits) are dropped, leaving credits to the chapters and
// analysis (owner, 2026-11-09), and a chapter intro that is the cold open
// beside an "OP" or "Opening" chapter is left out (ColdOpens; owner,
// 2026-11-10).
// theintrodb holds TheIntroDB's segments; analysis holds the chapter and
// analysis segments, told apart by Source; chapters are the probe's, by
// which the chapter intros are judged. The result is ordered by start and
// capped at 20.
//
// Every segment is first held to durationMs, the probe's runtime (Within):
// TheIntroDB times another cut of a title at will (smoke finding 6: an intro
// at 437-538 s on a 10 s file), so a source's segment past the end leaves
// its kind to the next source. Merge is where every source meets on its way
// to status.markers -- its one caller is the markers planner -- so the
// bound is kept here, not at each intake (ruling, smoke finding 6).
func Merge(theintrodb, analysis []Segment, chapters []commonv1.Chapter, durationMs int64) []Segment {
	theintrodb, analysis = Within(theintrodb, durationMs), WithoutColdOpens(Within(analysis, durationMs), chapters)
	var out []Segment
	for _, k := range kinds {
		tidb := of(theintrodb, k, "", 0)
		if k == KindCredits {
			tidb = plausibleCredits(tidb, introEnd(out), durationMs)
		}
		switch {
		case k == KindCredits && len(of(analysis, k, SourceChapters, ExactCreditsConfidence)) > 0:
			out = append(out, of(analysis, k, SourceChapters, ExactCreditsConfidence)...)
		case len(tidb) > 0:
			out = append(out, tidb...)
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

// implausibleCredits reports whether c cannot be a file's closing credits:
// it starts before the intro ends (introEndMs), or it covers more than half
// of a known runtime. TheIntroDB answers every first-season episode of
// To LOVE-Ru with credits {start 0, end null}, which read as the whole
// episode, so Plex offered to skip the credits as soon as the intro ended
// (owner, 2026-11-09: 21 of 26 episodes).
func implausibleCredits(c Segment, introEndMs, durationMs int64) bool {
	return c.StartMs < introEndMs || (durationMs > 0 && 2*(c.EndMs-c.StartMs) > durationMs)
}

// plausibleCredits is s without its implausible credits segments.
func plausibleCredits(s []Segment, introEndMs, durationMs int64) []Segment {
	out := s[:0:0]
	for _, x := range s {
		if !implausibleCredits(x, introEndMs, durationMs) {
			out = append(out, x)
		}
	}
	return out
}

// introEnd is the latest end of s's intro segments, 0 for none.
func introEnd(s []Segment) int64 {
	var end int64
	for _, x := range s {
		if x.Kind == KindIntro {
			end = max(end, x.EndMs)
		}
	}
	return end
}

// ImplausibleCredits reports whether stored -- status.markers' segments
// with their sources, held to durationMs -- holds a TheIntroDB credits
// segment Merge now drops, so the markers planner merges again though
// nothing else changed, as it does for a segment past the runtime.
func ImplausibleCredits(stored []Segment, durationMs int64) bool {
	end := introEnd(stored)
	for _, x := range Within(stored, durationMs) {
		if x.Kind == KindCredits && x.Source == SourceTheIntroDB && implausibleCredits(x, end, durationMs) {
			return true
		}
	}
	return false
}

// HasColdOpen reports whether stored -- status.markers' segments with
// their sources -- holds a chapter intro Merge now leaves out as the cold
// open beside the file's "OP" or "Opening" chapter, so the markers planner
// merges again though nothing else changed, as it does for implausible
// credits.
func HasColdOpen(stored []Segment, chapters []commonv1.Chapter) bool {
	return len(ColdOpens(stored, chapters)) > 0
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

// Within is s held to a file of durationMs: a segment starting at or past
// the end is dropped, and an end past it is clamped to it. A durationMs of
// zero or less (the runtime unknown) keeps s as it is.
func Within(s []Segment, durationMs int64) []Segment {
	if durationMs <= 0 {
		return s
	}
	out := make([]Segment, 0, len(s))
	for _, x := range s {
		if x.StartMs >= durationMs {
			continue
		}
		x.EndMs = min(x.EndMs, durationMs)
		out = append(out, x)
	}
	return out
}

// PastTheEnd reports whether s holds a segment Within would drop or clamp.
func PastTheEnd(s []Segment, durationMs int64) bool {
	if durationMs <= 0 {
		return false
	}
	for _, x := range s {
		if x.StartMs >= durationMs || x.EndMs > durationMs {
			return true
		}
	}
	return false
}
