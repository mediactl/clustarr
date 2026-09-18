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

package movie

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Phase computes the coarse lifecycle state of a Movie. A file already
// imported outranks availability entirely (hasFile is checked before
// !available), but never outranks the metadata-readiness gate: a stale
// reconcile with a leftover hasFile=true must not race ahead of Pending, so
// !metadataReady is checked before hasFile.
//
// pendingGrab reports whether status.pendingGrab is set -- a release the grab
// worker has chosen and is holding back for a DelayProfile's window. It
// outranks the file state for the same reason DownloadOverlay's Downloading
// does in the reconciler: Delayed and Downloading are one story, the in-flight
// grab, and the phase column should show the action in progress rather than
// the state it is about to replace. Without this arm Delayed is reachable only
// through an already-created Download, and the entire delay-profile feature is
// invisible -- an item sits at Wanted for the whole window.
//
// It ranks below the monitored and metadata gates: an unmonitored movie is
// unmonitored whatever the worker left behind, and a movie whose metadata is
// still refreshing is still Pending.
func Phase(monitored, metadataReady, available, hasFile, cutoffMet, pendingGrab bool) catalogv1alpha1.MoviePhase {
	switch {
	case !monitored:
		return catalogv1alpha1.MoviePhaseUnmonitored
	case !metadataReady:
		return catalogv1alpha1.MoviePhasePending
	case pendingGrab:
		return catalogv1alpha1.MoviePhaseDelayed
	case hasFile && !cutoffMet:
		return catalogv1alpha1.MoviePhaseCutoffUnmet
	case hasFile && cutoffMet:
		return catalogv1alpha1.MoviePhaseImported
	case !available:
		return catalogv1alpha1.MoviePhaseUnavailable
	default:
		return catalogv1alpha1.MoviePhaseWanted
	}
}
