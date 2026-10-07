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

// Overlay is the phase-independent verdict EntryOverlay derives from a grab
// entry's phase. It exists because Movie and Episode each have their own
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
