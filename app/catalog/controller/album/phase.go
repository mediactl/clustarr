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

package album

import (
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Phase computes the coarse lifecycle state of an Album, mirroring
// episode.Phase's rank (Imported > pendingGrab-as-Delayed > CutoffUnmet >
// Wanted, gated by monitored) minus the air-date gate: album_types.go's
// AlbumPhase enum has no Unaired member (a release group is either already
// released or not, but Album carries no independent "has this come out
// yet" signal of its own the way Episode's airDate does -- that lives on
// status.metadata.releaseDate, gateway-owned), so an unmonitored-but-
// unreleased album simply reads Wanted like any other not-yet-imported one.
// Deciding whether to search/grab an unreleased album at all is the search
// worker's job (app/catalog/worker/search), which does not support non-video
// kinds yet -- a separate subsystem this task's controllers do not touch,
// unrelated to whether pkg/quality itself has a music ladder (it does; see
// this package's reconciler.go doc comment).
//
// pendingGrab reports whether status.pendingGrab is set (read-only here;
// see this package's doc comment for why this reconciler never writes it).
func Phase(monitored, hasFile, cutoffMet, pendingGrab bool) commonv1.LeafPhase {
	switch {
	case !monitored:
		return commonv1.LeafPhaseUnmonitored
	case hasFile && cutoffMet:
		return commonv1.LeafPhaseImported
	case pendingGrab:
		return commonv1.LeafPhaseDelayed
	case hasFile:
		return commonv1.LeafPhaseCutoffUnmet
	default:
		return commonv1.LeafPhaseWanted
	}
}
