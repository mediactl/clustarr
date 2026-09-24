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
	//
	// DownloadOverlay no longer returns it (gap-fix R-12, below): Delayed is
	// spec §8.2's delay-profile hold, status.pendingGrab, which each item's
	// Phase function reads directly, and it ends when the grab creates a
	// Download. The value stays because the per-kind adapters name it.
	OverlayDelayed
	// OverlayDownloading maps to the item's own Downloading phase constant.
	OverlayDownloading
)

// DownloadOverlay reads the Download an item's status.activeDownloadRef
// names and decides whether the item's Phase is overridden, and whether the
// ref stays set. dl is nil when the item has no such Download.
//
// # The mapping (gap-fix R-12, re-read once grabarr drove real Downloads)
//
// Every Download that is not terminal (DownloadNonTerminal) overlays
// Downloading, and is active; every terminal one has no opinion, and is
// not. Phase by phase:
//
//	""           Downloading  created by the grab; grabarr has not seen it yet
//	Pending      Downloading  grabbed and queued; no client picked yet
//	Assigned     Downloading  handed to a client
//	Queued       Downloading  in the client's queue
//	Downloading  Downloading  bytes moving
//	Paused       Downloading  still the item's download, merely held
//	Completed    Downloading  on disk, waiting for importarr
//	Seeding      Downloading  on disk, not imported (Imported beats Seeding)
//	Imported     none         the MediaFile rollup says Imported/CutoffUnmet
//	Failed       none         Wanted again: §8.3 re-searches it
//	Blocklisted  none         likewise
//	Removing     none         on its way out
//	(deleting)   none         likewise, whatever the phase
//
// The item phase enums (spec §4.2) have no Queued or Importing member:
// Downloading is the one "a grab is in flight" state, spanning the whole
// stretch from the grab to the import that sets Imported or CutoffUnmet
// (§8.4). That matches Radarr, whose QueueSpecification treats every queue
// entry except one in FailedPending -- ImportPending and Importing
// included -- as the movie already being downloaded
// (https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/DecisionEngine/Specifications/QueueSpecification.cs).
//
// Two rows changed from the Phase C table, which was written before
// anything drove a Download past Assigned:
//
//   - Completed and Seeding used to have no opinion, so an item whose file
//     was one import away read Wanted -- and the wanted sweep, which selects
//     Wanted and CutoffUnmet (app/catalog/controller/wantedcron), searched
//     for it again. Now they are Downloading, which it skips.
//   - Pending used to map to Delayed, "the closest existing value". But
//     Delayed means a grab held back by a DelayProfile (§8.2, PendingGrab),
//     which is over by the time a Download exists; a Pending Download is
//     queued, not delayed.
//
// With every non-terminal phase overlaying and every terminal one not, the
// overlay and the ref are now one test (DownloadNonTerminal), so an item
// never shows Downloading without an activeDownloadRef or the reverse.
func DownloadOverlay(dl *downloadv1alpha1.Download) (overlay Overlay, active bool) {
	if !DownloadNonTerminal(dl) {
		return OverlayNone, false
	}
	return OverlayDownloading, true
}
