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

package episode

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// DownloadOverlay maps rollup.DownloadOverlay's phase-independent verdict
// onto EpisodePhase. The decision logic (which Download phases mean what)
// lives once, in app/catalog/controller/rollup, per the C6 controller
// amendment -- this is only the type translation, not a second copy of the
// switch. See rollup.DownloadOverlay's doc comment for the mapping
// rationale.
func DownloadOverlay(dl *downloadv1alpha1.Download) (phase catalogv1alpha1.EpisodePhase, active bool) {
	overlay, active := rollup.DownloadOverlay(dl)
	switch overlay {
	case rollup.OverlayDownloading:
		return catalogv1alpha1.EpisodePhaseDownloading, active
	default:
		return "", active
	}
}
