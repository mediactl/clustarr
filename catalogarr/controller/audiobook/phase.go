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

package audiobook

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Phase computes the coarse lifecycle state of an Audiobook. Its shape
// mirrors movie.Phase (hasFile-and-cutoffMet outranks a pending grab, which
// outranks a bare hasFile, which outranks the default Wanted), but it takes
// no metadataReady or available argument -- AudiobookPhase's kubebuilder enum
// (Wanted;Delayed;Downloading;Imported;CutoffUnmet;Unmonitored) has neither a
// Pending value for "metadata not fetched yet" nor an Unavailable value for
// "not released yet", unlike MoviePhase and unlike EpisodePhase's Unaired.
// There is no minimumAvailability concept for an Audible recording, so the
// missing Unavailable value is expected; the missing Pending value looks
// like an oversight against Movie's shape, but the type is frozen (spec
// §4.2) and inventing a phase value the CRD's Enum validation rejects is not
// an option. The reconciler folds "metadata not yet fetched" into the
// ordinary Wanted bucket instead (an audiobook with no cached title still
// reads as "wanted", which is not false) and reports the gap on
// AudiobookConditionMetadataReady, the condition this state actually
// belongs on.
func Phase(monitored, hasFile, cutoffMet, pendingGrab bool) catalogv1alpha1.AudiobookPhase {
	switch {
	case !monitored:
		return catalogv1alpha1.AudiobookPhaseUnmonitored
	case hasFile && cutoffMet:
		return catalogv1alpha1.AudiobookPhaseImported
	case pendingGrab:
		return catalogv1alpha1.AudiobookPhaseDelayed
	case hasFile:
		return catalogv1alpha1.AudiobookPhaseCutoffUnmet
	default:
		return catalogv1alpha1.AudiobookPhaseWanted
	}
}
