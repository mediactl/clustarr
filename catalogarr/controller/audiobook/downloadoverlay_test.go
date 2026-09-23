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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/audiobook"
)

// TestDownloadOverlay pins the AudiobookPhase translation of
// rollup.DownloadOverlay over every Download phase, under the R-12 contract
// (3d3cf54): every phase before Imported reads Downloading and keeps the
// ref; only a terminal phase or a pending deletion clears it.
func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	deleting := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		d := dl(p)
		now := metav1.Now()
		d.DeletionTimestamp = &now
		return d
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantPhase  catalogv1alpha1.AudiobookPhase
		wantActive bool
	}{
		{"nil download, no opinion", nil, "", false},
		{"created by the grab, not yet seen by grabarr", dl(""), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"pending is queued, not delayed: Downloading", dl(downloadv1alpha1.DownloadPhasePending), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"assigned maps to Downloading", dl(downloadv1alpha1.DownloadPhaseAssigned), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"queued maps to Downloading", dl(downloadv1alpha1.DownloadPhaseQueued), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"downloading maps to Downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"paused maps to Downloading", dl(downloadv1alpha1.DownloadPhasePaused), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"completed is on disk awaiting import: Downloading", dl(downloadv1alpha1.DownloadPhaseCompleted), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"seeding is not imported yet: Downloading", dl(downloadv1alpha1.DownloadPhaseSeeding), catalogv1alpha1.AudiobookPhaseDownloading, true},
		{"imported clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseImported), "", false},
		{"failed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseFailed), "", false},
		{"blocklisted clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), "", false},
		{"removing clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseRemoving), "", false},
		{"a Download being deleted clears the ref, whatever its phase", deleting(downloadv1alpha1.DownloadPhaseDownloading), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := audiobook.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantPhase, phase)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
