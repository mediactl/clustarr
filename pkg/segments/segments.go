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

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// AnalyzerVersion is recorded in status.markers.analysis.version; raise it
// when detection changes, and every file is analyzed once more. 2: credits
// reach the end past the last keyframe, the DNN is asked whenever no
// candidate holds, chapters are trusted by kind, anime EDs end before their
// preview (2026-10-01). 3: a credits chapter wholly titled as credits is
// 100 and outranks TheIntroDB, any other chapter 90 (2026-10-01).
const AnalyzerVersion int32 = 3

// FingerprintVersion versions the clustarr-fingerprints cache keys. 1 stands
// for the unversioned keys the ffmpeg(1) decoder wrote; 2 is the in-process
// ffgo decoder (spec 2026-10-06 §7.2.7, OD48). Raise it whenever a cached
// fingerprint would differ: decode output (pkg/segments/decode, the fork's
// decode path, or the native image's libavcodec minor), the window lengths
// (startWindow, endWindow) or the Chromaprint configuration (split §7.2.7 as
// amended 2026-10-07). A detection-only change raises AnalyzerVersion alone
// and keeps the cache. Old objects age out under the store's 90-day MaxAge.
const FingerprintVersion int32 = 2

// FingerprintKey is a window's object name in the fingerprint cache: the only
// builder of one (the bucket's key scheme is events.FingerprintKeyScheme,
// recorded in its metadata). It panics on an empty part or one holding '/'
// or '.', which would forge a segment of the name: both parts are values the
// caller controls (a probe hash, "start" or "end"), never user input.
func FingerprintKey(probeHash, which string) string {
	return fingerprintKeyAt(FingerprintVersion, probeHash, which)
}

// fingerprintKeyAt is FingerprintKey at version v: the legacy unversioned
// "<probeHash>.<which>" at 1, "<probeHash>.<which>.v<N>" from 2 on.
func fingerprintKeyAt(v int32, probeHash, which string) string {
	for _, p := range []string{probeHash, which} {
		if p == "" || strings.ContainsAny(p, "/.") {
			panic(fmt.Sprintf("segments: fingerprint key part %q is empty or holds '/' or '.'", p))
		}
	}
	if v <= 1 {
		return probeHash + "." + which
	}
	return probeHash + "." + which + ".v" + strconv.Itoa(int(v))
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

// RecordSchema is a v2 record's Schema; a v1 record has none.
const RecordSchema = "segments.Record.v2"

// Attempt is an analysis that failed.
type Attempt struct {
	ProbeHash string    `json:"probeHash"`
	Version   int32     `json:"version"`
	Message   string    `json:"message"`
	At        time.Time `json:"at"`
}

// Record is a file's analysis in the clustarr-segments bucket, keyed
// events.RecordKey(MediaFile UID), written by cmd/markers by CAS (Store;
// loop spec 2026-10-06 §4.12): what the remediation loop's markers planner
// merges into status.markers, and what the worker reads to skip a file
// already analyzed or to add an intro its season later revealed. A v1
// record has no Schema, File, AnalyzedAt, Amend or LastError.
type Record struct {
	Schema     string     `json:"schema,omitempty"`
	File       schema.Ref `json:"file,omitzero"`
	ProbeHash  string     `json:"probeHash"`
	Version    int32      `json:"version"`
	Result     string     `json:"result"`
	Segments   []Segment  `json:"segments"`
	AnalyzedAt time.Time  `json:"analyzedAt,omitzero"`
	// Amend counts the season amendments (an intro the season revealed
	// later) made to this analysis.
	Amend     int32    `json:"amend,omitempty"`
	LastError *Attempt `json:"lastError,omitempty"`
}
