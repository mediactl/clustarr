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

package views

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The episode page's Status fact says a file's state in the movie page's
// words; a transcoded file reads Transcoded from the catalog's cutoff
// reason whatever the phase (talos, 2026-11-04: Andor's unmonitored
// S01E01, phase Unmonitored, read Downloaded).
func TestEpisodeStatusLabel(t *testing.T) {
	file := RowStatus{Kind: "file", Label: "WEBDL-1080p"}
	warn := RowStatus{Kind: "file", Label: "WEBDL-1080p", Warn: true}
	for name, tc := range map[string]struct {
		status       RowStatus
		phase        string
		cutoffReason string
		want         string
		stripe       string
	}{
		"downloaded":                 {file, "Imported", "CutoffMet", "Downloaded", "downloaded"},
		"transcoded":                 {file, "Transcoded", "Transcoded", "Transcoded", "downloaded"},
		"transcoded, unmonitored":    {file, "Unmonitored", "Transcoded", "Transcoded", "downloaded"},
		"cutoff unmet":               {warn, "CutoffUnmet", "CutoffUnmet", "Cutoff Unmet", "cutoff-unmet"},
		"cutoff unmet with a reason": {warn, "CutoffUnmet", "MissingAudio", "Cutoff Unmet: MissingAudio", "cutoff-unmet"},
		"missing":                    {RowStatus{Kind: "missing", Label: "Missing"}, "Wanted", "Pending", "Missing", "missing"},
		"pending grab":               {RowStatus{Kind: "pending", Label: "Pending"}, "Delayed", "Pending", "Pending", "downloading"},
	} {
		v := EpisodeView{Row: EpisodeRow{Status: tc.status, Phase: tc.phase}, CutoffReason: tc.cutoffReason}
		assert.Equal(t, tc.want, episodeStatusLabel(v), name)
		assert.Equal(t, tc.stripe, episodeStripe(tc.status), name)
	}
}

// A pager lists every page of up to seven, else the first, the last and
// the open one's neighbours, a gap (0) between.
func TestListPageWindow(t *testing.T) {
	assert.Equal(t, []int{1, 2, 3}, listPageWindow(ListPage{Page: 2, Pages: 3}))
	assert.Equal(t, []int{1, 0, 9, 10, 11, 0, 20}, listPageWindow(ListPage{Page: 10, Pages: 20}))
	assert.Equal(t, []int{1, 2, 0, 20}, listPageWindow(ListPage{Page: 1, Pages: 20}))
}
