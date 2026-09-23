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

package audiobook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/audiobook"
)

func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantPhase  catalogv1alpha1.AudiobookPhase
		wantActive bool
	}{
		{"nil download, no opinion", nil, "", false},
		{"pending maps to Delayed", dl(downloadv1alpha1.DownloadPhasePending), catalogv1alpha1.AudiobookPhaseDelayed, true},
		{"assigned maps to Downloading", dl(downloadv1alpha1.DownloadPhaseAssigned), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"queued maps to Downloading", dl(downloadv1alpha1.DownloadPhaseQueued), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"downloading maps to Downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"paused maps to Downloading", dl(downloadv1alpha1.DownloadPhasePaused), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"completed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseCompleted), "", false},
		{"seeding clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseSeeding), "", false},
		{"imported clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseImported), "", false},
		{"failed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseFailed), "", false},
		{"blocklisted clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), "", false},
		{"removing clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseRemoving), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := audiobook.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantPhase, phase)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
