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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Phase computes the coarse lifecycle state of an Episode. A file already
// imported outranks the air-date gate entirely -- unlike Movie's Pending
// gate, Episode has no metadata-readiness precondition of its own to race
// against, so a file for a future-dated episode (a scene leak, a re-air) is
// a real, if unusual, case that still reports Imported/CutoffUnmet.
//
// pendingGrab reports whether status.pendingGrab is set -- a release the grab
// worker has chosen and is holding back for a DelayProfile's window. Without
// this arm Delayed is reachable only through an already-created Download and
// the delay-profile feature is invisible.
//
// Its rank matches the movie package's, for the same reasons: above
// CutoffUnmet, Unaired and Wanted, where the pending grab is the story, and
// below Imported and the monitored gate. A pending grab is an internal timer,
// not an object the user can see or cancel, so it does not displace a good
// cutoff-met file the way an actual Download does. Above Unaired is
// deliberate: a pending grab for an unaired episode means a release leaked
// early, and reporting Unaired while a grab is scheduled would be a lie.
//
// cutoffEvaluated reports whether the Series' QualityProfile resolved, i.e.
// whether cutoffMet is a verdict at all; a file never ranked reads
// CutoffUnevaluated at CutoffUnmet's rank (see the movie package's Phase).
func Phase(monitored bool, airDate *metav1.Time, hasFile, cutoffMet, cutoffEvaluated, pendingGrab bool, now time.Time) catalogv1alpha1.EpisodePhase {
	switch {
	case !monitored:
		return catalogv1alpha1.EpisodePhaseUnmonitored
	case hasFile && cutoffMet:
		return catalogv1alpha1.EpisodePhaseImported
	case pendingGrab:
		return catalogv1alpha1.EpisodePhaseDelayed
	case hasFile && !cutoffEvaluated:
		return catalogv1alpha1.EpisodePhaseCutoffUnevaluated
	case hasFile:
		return catalogv1alpha1.EpisodePhaseCutoffUnmet
	case airDate == nil || now.Before(airDate.Time):
		return catalogv1alpha1.EpisodePhaseUnaired
	default:
		return catalogv1alpha1.EpisodePhaseWanted
	}
}
