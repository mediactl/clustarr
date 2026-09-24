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

package movie_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
)

// TestDownloadOverlay proves only the MoviePhase type mapping --
// rollup.DownloadOverlay's decision table (which Download phases mean
// what) is table-tested once, in app/catalog/controller/rollup, per the C6
// controller amendment.
func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantPhase  catalogv1alpha1.MoviePhase
		wantActive bool
	}{
		{"nil download, nothing to say", nil, "", false},
		{"pending is queued, not delayed", dl(downloadv1alpha1.DownloadPhasePending), catalogv1alpha1.MoviePhaseDownloading, true},
		{"assigned", dl(downloadv1alpha1.DownloadPhaseAssigned), catalogv1alpha1.MoviePhaseDownloading, true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), catalogv1alpha1.MoviePhaseDownloading, true},
		{"completed is awaiting import, not missing", dl(downloadv1alpha1.DownloadPhaseCompleted), catalogv1alpha1.MoviePhaseDownloading, true},
		{"imported defers to the file", dl(downloadv1alpha1.DownloadPhaseImported), "", false},
		{"failed defers", dl(downloadv1alpha1.DownloadPhaseFailed), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := movie.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantPhase, phase)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
