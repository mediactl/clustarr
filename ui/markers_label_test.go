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

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The files table names where each segment came from, and shows segments
// clustarr found itself even where TheIntroDB has none.
func TestMarkersLabelNamesTheSource(t *testing.T) {
	assert.Equal(t, "", markersLabel(nil))
	assert.Equal(t, "intro 1:00–1:30 (TheIntroDB), credits 43:20–45:00 (detected)", markersLabel(&catalogv1.FileMarkers{
		Result: catalogv1.MarkersFound,
		Segments: []catalogv1.MarkerSegment{
			{Kind: catalogv1.MarkerIntro, StartMs: 60_000, EndMs: 90_000, Source: catalogv1.SegmentSourceTheIntroDB},
			{Kind: catalogv1.MarkerCredits, StartMs: 2_600_000, EndMs: 2_700_000, Source: catalogv1.SegmentSourceAnalysis},
		},
	}))
	assert.Equal(t, "recap 0:00–0:45 (chapters)", markersLabel(&catalogv1.FileMarkers{
		Result:   catalogv1.MarkersNotFound,
		Segments: []catalogv1.MarkerSegment{{Kind: catalogv1.MarkerRecap, StartMs: 0, EndMs: 45_000, Source: catalogv1.SegmentSourceChapters}},
	}), "TheIntroDB's NotFound does not hide what was found locally")
	assert.Equal(t, "none found", markersLabel(&catalogv1.FileMarkers{
		Result:   catalogv1.MarkersNotFound,
		Analysis: &catalogv1.SegmentAnalysis{Result: catalogv1.MarkersNotFound},
	}))
	assert.Equal(t, "TheIntroDB unavailable", markersLabel(&catalogv1.FileMarkers{Result: catalogv1.MarkersError}))
	assert.Equal(t, "intro 0:10–0:40 (TheIntroDB)", markersLabel(&catalogv1.FileMarkers{
		Result: catalogv1.MarkersFound, Segments: []catalogv1.MarkerSegment{{Kind: catalogv1.MarkerIntro, StartMs: 10_000, EndMs: 40_000}},
	}), "an untagged segment is TheIntroDB's")
}
