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

package book_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/book"
)

// TestDownloadOverlay proves only the BookPhase type mapping --
// rollup.DownloadOverlay's decision table (which Download phases mean what)
// is table-tested once, in catalogarr/controller/rollup, per the C6
// controller amendment. Mirrors movie/downloadoverlay_test.go exactly.
func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantPhase  catalogv1alpha1.BookPhase
		wantActive bool
	}{
		{"nil download, nothing to say", nil, "", false},
		{"pending has no engine yet, closest fit is delayed", dl(downloadv1alpha1.DownloadPhasePending), catalogv1alpha1.BookPhaseDelayed, true},
		{"assigned is actively working", dl(downloadv1alpha1.DownloadPhaseAssigned), catalogv1alpha1.BookPhaseDownloading, true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), catalogv1alpha1.BookPhaseDownloading, true},
		{"paused still counts as downloading", dl(downloadv1alpha1.DownloadPhasePaused), catalogv1alpha1.BookPhaseDownloading, true},
		{"completed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseCompleted), "", false},
		{"failed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseFailed), "", false},
		{"blocklisted clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := book.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantPhase, phase)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
