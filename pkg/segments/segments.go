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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// AnalyzerVersion is recorded in status.markers.analysis.version; raise it
// when detection changes, and every file is analyzed once more.
const AnalyzerVersion int32 = 1

// Segment is one detected or fetched segment.
type Segment struct {
	Kind       catalogv1alpha1.MarkerKind
	StartMs    int64
	EndMs      int64
	Source     catalogv1alpha1.SegmentSource
	Confidence int32
}
