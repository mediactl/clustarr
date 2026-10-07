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

// Package segments decides a file's skip segments from what clustarr
// detects itself -- chapters, shared audio, end-of-file frames, text
// density -- and merges them under TheIntroDB's (spec 2026-10-01 segment
// detection). Its subpackages do the detecting.
package segments

import "strconv"

// AnalyzerVersion is recorded in status.markers.analysis.version; raise it
// when detection changes, and every file is analyzed once more. 2: credits
// reach the end past the last keyframe, the DNN is asked whenever no
// candidate holds, chapters are trusted by kind, anime EDs end before their
// preview (2026-10-01). 3: a credits chapter wholly titled as credits is
// 100 and outranks TheIntroDB, any other chapter 90 (2026-10-01).
const AnalyzerVersion int32 = 3

// FingerprintVersion versions the clustarr-fingerprints cache keys. 1 stands
// for the unversioned keys the ffmpeg(1) decoder wrote; 2 is the in-process
// ffgo decoder (spec 2026-10-06 §7.2.7, OD48). Raise it whenever decode
// output can change: pkg/segments/decode, the fork's decode path, or the
// native image's libavcodec minor. Old objects age out under the store's
// 90-day MaxAge.
const FingerprintVersion int32 = 2

// FingerprintKey is a window's object name in the fingerprint cache.
func FingerprintKey(probeHash, which string) string {
	return probeHash + "." + which + ".v" + strconv.Itoa(int(FingerprintVersion))
}

// Kind is a segment's kind. The values are api/catalog/v1alpha1.MarkerKind's
// (TestLocalEnumsAreTheAPIs); they are declared here so cmd/markers links no
// Kubernetes API types (spec 2026-10-06 §7.2.9).
type Kind string

// Kinds, as MarkerKind spells them.
const (
	KindIntro   Kind = "intro"
	KindRecap   Kind = "recap"
	KindCredits Kind = "credits"
	KindPreview Kind = "preview"
)

// Source is where a segment came from, as SegmentSource spells it.
type Source string

// Sources.
const (
	SourceTheIntroDB Source = "theintrodb"
	SourceChapters   Source = "chapters"
	SourceAnalysis   Source = "analysis"
)

// Results of an analysis, as MarkersResult spells them.
const (
	ResultFound    = "Found"
	ResultNotFound = "NotFound"
	ResultError    = "Error"
)

// Segment is one detected or fetched segment.
type Segment struct {
	Kind       Kind   `json:"kind"`
	StartMs    int64  `json:"startMs"`
	EndMs      int64  `json:"endMs"`
	Source     Source `json:"source"`
	Confidence int32  `json:"confidence"`
}

// Record is a file's analysis as kept in the clustarr-segments bucket,
// keyed by its MediaFile's UID: what the merge into status.markers needs
// when TheIntroDB's side changes, and what segmentarr-worker reads to skip
// a file already analyzed or to add an intro its season later revealed.
type Record struct {
	ProbeHash string    `json:"probeHash"`
	Version   int32     `json:"version"`
	Result    string    `json:"result"`
	Segments  []Segment `json:"segments"`
}
