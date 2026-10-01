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

// A file's row lists its skip segments as TheIntroDB gave them, in order,
// and says so when TheIntroDB has none.
func TestFileRowListsTheFilesMarkers(t *testing.T) {
	f := &catalogv1.MediaFile{}
	f.Spec.Path = "/data/media/tv/Breaking Bad/S01E01.mkv"
	f.Status.Markers = &catalogv1.FileMarkers{Result: catalogv1.MarkersFound, Segments: []catalogv1.MarkerSegment{
		{Kind: catalogv1.MarkerIntro, StartMs: 228664, EndMs: 246143},
		{Kind: catalogv1.MarkerCredits, StartMs: 3431000, EndMs: 3480000},
	}}
	rows, _ := fileRows("/data/media/tv/Breaking Bad", f)
	assert.Equal(t, "intro 3:48–4:06 (TheIntroDB), credits 57:11–58:00 (TheIntroDB)", rows[0].Markers)

	f.Status.Markers = &catalogv1.FileMarkers{Result: catalogv1.MarkersNotFound}
	rows, _ = fileRows("", f)
	assert.Equal(t, "none found", rows[0].Markers)

	f.Status.Markers = nil
	rows, _ = fileRows("", f)
	assert.Empty(t, rows[0].Markers)
}
