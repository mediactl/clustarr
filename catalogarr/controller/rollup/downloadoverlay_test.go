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

package rollup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
)

func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantClass  rollup.Overlay
		wantActive bool
	}{
		{"nil download, nothing to say", nil, rollup.OverlayNone, false},
		{"pending has no engine yet, closest fit is delayed", dl(downloadv1alpha1.DownloadPhasePending), rollup.OverlayDelayed, true},
		{"assigned is actively working", dl(downloadv1alpha1.DownloadPhaseAssigned), rollup.OverlayDownloading, true},
		{"queued is actively working", dl(downloadv1alpha1.DownloadPhaseQueued), rollup.OverlayDownloading, true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), rollup.OverlayDownloading, true},
		{"paused still counts as downloading", dl(downloadv1alpha1.DownloadPhasePaused), rollup.OverlayDownloading, true},
		{"completed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseCompleted), rollup.OverlayNone, false},
		{"seeding clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseSeeding), rollup.OverlayNone, false},
		{"imported clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseImported), rollup.OverlayNone, false},
		{"failed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseFailed), rollup.OverlayNone, false},
		{"blocklisted clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), rollup.OverlayNone, false},
		{"removing clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseRemoving), rollup.OverlayNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			overlay, active := rollup.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantClass, overlay)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
