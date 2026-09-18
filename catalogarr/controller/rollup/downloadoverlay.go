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

package rollup

import (
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// Overlay is the phase-independent verdict DownloadOverlay derives from a
// Download's phase. It exists because Movie and Episode each have their own
// phase enum (MoviePhase, EpisodePhase); the movie and episode packages
// translate this into their own type with a one-line switch, so the
// decision logic below is written once.
type Overlay int

// Overlay values.
const (
	// OverlayNone means "no opinion, let the ordinary availability/file
	// computation decide".
	OverlayNone Overlay = iota
	// OverlayDelayed maps to the item's own Delayed phase constant.
	OverlayDelayed
	// OverlayDownloading maps to the item's own Downloading phase constant.
	OverlayDownloading
)

// DownloadOverlay reads dl's phase and decides whether the item's Phase
// should be overridden and whether ActiveDownloadRef should stay set. dl is
// nil once the ref has been cleared or was never set; OverlayNone means "no
// opinion, let the ordinary availability/file computation decide".
//
// The mapping is a judgment call, flagged in review: Movie/Episode's phase
// enum was designed around the *arr search/grab/import lifecycle, not
// Download's own phases, and the two do not line up one-to-one.
// DownloadPhasePending has no engine assigned yet, so the closest existing
// value is Delayed; Assigned/Queued/Downloading/Paused are all "actively
// working on it" and map to Downloading; every terminal phase
// (Completed/Seeding/Imported/Failed/Blocklisted/Removing) clears the ref
// and returns no opinion, deferring to the MediaFile watch (which produces
// Imported once the file actually lands) or the ordinary Wanted/Unavailable
// computation. Nothing in Phase C ever drives a Download past
// Pending/Assigned -- grabarr, the only writer past that point, is Phase D
// -- so only the first two rows are exercised by anything real before then;
// the rest are unit-tested against synthetic fixtures now and wired for
// when grabarr lands.
func DownloadOverlay(dl *downloadv1alpha1.Download) (overlay Overlay, active bool) {
	if dl == nil {
		return OverlayNone, false
	}
	switch dl.Status.Phase {
	case downloadv1alpha1.DownloadPhasePending:
		return OverlayDelayed, true
	case downloadv1alpha1.DownloadPhaseAssigned, downloadv1alpha1.DownloadPhaseQueued,
		downloadv1alpha1.DownloadPhaseDownloading, downloadv1alpha1.DownloadPhasePaused:
		return OverlayDownloading, true
	default: // Completed, Seeding, Imported, Failed, Blocklisted, Removing
		return OverlayNone, false
	}
}
