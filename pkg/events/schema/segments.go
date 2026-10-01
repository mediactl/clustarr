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

package schema

import commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

// SegmentsPlanTask asks catalogarr's planner to build one analysis task:
// a season's (Series and Season) or a movie's file (Movie, the MediaFile's
// name). Subject: clustarr.work.catalogarr.segments-plan.normal.<key>.
type SegmentsPlanTask struct {
	Namespace string `json:"namespace"`
	Series    string `json:"series,omitempty"`
	Season    int32  `json:"season,omitempty"`
	Movie     string `json:"movie,omitempty"`
}

// Schema implements Payload.
func (SegmentsPlanTask) Schema() string { return "catalog.SegmentsPlanTask.v1" }

// AnalyzeFile is one file of an AnalyzeTask.
type AnalyzeFile struct {
	MediaFile   string             `json:"mediaFile"`
	UID         string             `json:"uid"`
	Path        string             `json:"path"`
	ProbeHash   string             `json:"probeHash"`
	DurationMs  int64              `json:"durationMs"`
	AudioStream int32              `json:"audioStream,omitempty"`
	Chapters    []commonv1.Chapter `json:"chapters,omitempty"`
	// Due is false for a file analyzed already: it is fingerprinted only,
	// as its season's comparison.
	Due bool `json:"due,omitempty"`
	// Anime gets a preview after its credits.
	Anime bool `json:"anime,omitempty"`
}

// AnalyzeTask is segmentarr-worker's task: a season's files in episode
// order (Kind episode) or one movie's (Kind movie). Subject:
// clustarr.work.catalogarr.segments-analyze.normal.<key>.
type AnalyzeTask struct {
	Namespace string        `json:"namespace"`
	Key       string        `json:"key"`
	Kind      string        `json:"kind"`
	Files     []AnalyzeFile `json:"files"`
}

// Schema implements Payload.
func (AnalyzeTask) Schema() string { return "catalog.AnalyzeTask.v1" }

// SegmentJSON is one segment of a SegmentsResult.
type SegmentJSON struct {
	Kind       string `json:"kind"`
	StartMs    int64  `json:"startMs"`
	EndMs      int64  `json:"endMs"`
	Source     string `json:"source"`
	Confidence int32  `json:"confidence"`
}

// SegmentsResult is one file's analysis, from segmentarr-worker. Subject:
// clustarr.work.catalogarr.segments-result.normal.<key>; the envelope key
// is <namespace>/<name> of the MediaFile.
type SegmentsResult struct {
	MediaFile string        `json:"mediaFile"`
	ProbeHash string        `json:"probeHash"`
	Version   int32         `json:"version"`
	Result    string        `json:"result"`
	Message   string        `json:"message,omitempty"`
	Segments  []SegmentJSON `json:"segments,omitempty"`
}

// Schema implements Payload.
func (SegmentsResult) Schema() string { return "catalog.SegmentsResult.v1" }
