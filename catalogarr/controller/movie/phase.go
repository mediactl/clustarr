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
// worker has chosen and is holding back for a DelayProfile's window. Without
// this arm Delayed is reachable only through an already-created Download, and
// the entire delay-profile feature is invisible: an item sits at Wanted for
// the whole window.
//
// Its rank is deliberate and narrow. It sits ABOVE CutoffUnmet, Unavailable
// and Wanted, because in each of those the pending grab IS the story: an
// upgrade is scheduled, and reporting "still wanted" hides it. It sits BELOW
// Imported, and below the monitored and metadata gates. Imported is the
// distinction that matters: a pending grab is an internal timer, not an object
// the user can see or cancel -- unlike a Download, which is why
// DownloadOverlay's Downloading is allowed to override Imported. The phase
// column is the item's state, not the pipeline's, and a user with a good
// cutoff-met file should read Imported.
//
// cutoffEvaluated reports whether the QualityProfile resolved, i.e. whether
// cutoffMet is a verdict at all. A file whose cutoff was never evaluated
// reads CutoffUnevaluated rather than CutoffUnmet: the latter means "below
// the cutoff, an upgrade is wanted" and puts the item in the cutoff-unmet
// search rotation, which a dangling or unparseable profile must not do. It
// takes CutoffUnmet's rank exactly, since it answers the same question.
func Phase(monitored, metadataReady, available, hasFile, cutoffMet, cutoffEvaluated, pendingGrab bool) catalogv1alpha1.MoviePhase {
	switch {
	case !monitored:
		return catalogv1alpha1.MoviePhaseUnmonitored
	case !metadataReady:
		return catalogv1alpha1.MoviePhasePending
	case hasFile && cutoffMet:
		return catalogv1alpha1.MoviePhaseImported
	case pendingGrab:
		return catalogv1alpha1.MoviePhaseDelayed
	case hasFile && !cutoffEvaluated:
		return catalogv1alpha1.MoviePhaseCutoffUnevaluated
	case hasFile:
		return catalogv1alpha1.MoviePhaseCutoffUnmet
	case !available:
		return catalogv1alpha1.MoviePhaseUnavailable
	default:
		return catalogv1alpha1.MoviePhaseWanted
	}
}
