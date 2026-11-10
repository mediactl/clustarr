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
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// RetiredLeafConditions are the conditions every non-video leaf -- an
// Album, Book, Audiobook or Issue -- carried until 2026-11-10 (design D4)
// and that restated a status field: HasFile said status.hasFile, CutoffMet
// said status.cutoffMet with status.cutoffReason's reason, Released said
// the phase's Unavailable. Ready and DeadLettered stay, as do the
// conditions that report work (MetadataReady, QueueFull, TracksSynced,
// Invalid, BookRefResolved). The Episode retired its four the same way on
// 2026-11-03 (episode.retiredEpisodeConditions).
var RetiredLeafConditions = map[string]bool{"HasFile": true, "CutoffMet": true, "Released": true}

// DropRetiredLeafConditions is conditions without the RetiredLeafConditions,
// on a copy, so a leaf's first pass stops sending them and the apiserver
// removes them: an apply that copied them from the cache would keep them
// for good, since every apply is the manager's complete declaration.
func DropRetiredLeafConditions(conditions []metav1.Condition) []metav1.Condition {
	return slices.DeleteFunc(slices.Clone(conditions), func(c metav1.Condition) bool {
		return RetiredLeafConditions[c.Type]
	})
}

// LeafOverlay maps a leaf's active grab entry onto the one LeafPhase the
// four non-video leaves share (EntryOverlay): Downloading while an entry is
// live, "" when the ordinary computation decides, and whether an entry is
// active at all (status.activeDownloadRef). Delayed is the leaf's own
// Pending-grab arm (LeafPhase), which EntryOverlay no longer returns.
func LeafOverlay(dl *catalogv1alpha1.DownloadEntry) (phase commonv1.LeafPhase, active bool) {
	overlay, active := EntryOverlay(dl)
	switch overlay {
	case OverlayDelayed:
		return commonv1.LeafPhaseDelayed, active
	case OverlayDownloading:
		return commonv1.LeafPhaseDownloading, active
	default:
		return "", active
	}
}

// LeafIdle is what a non-video leaf's watch reads off the object the cache
// lists at a manager's start to decide whether its pass would change
// nothing (Watch.SkipInitial, 2026-11-10 design D14; the Episode's
// idleAtStart of 2026-10-28).
type LeafIdle struct {
	// Generation and ObservedGeneration: a spec not yet observed has work.
	Generation, ObservedGeneration int64
	// Monitored is the item's own flag and its container's
	// (itemread.Monitored), as the pass would read it.
	Monitored bool
	// Phase is status.phase as last written.
	Phase commonv1.LeafPhase
	// RefreshDue is whether the pass would ask for a metadata refresh: the
	// document missing, past its RefreshTTL or older than
	// metadata.SchemaVersion. An Issue carries no document of its own.
	RefreshDue bool
	// ActiveDownloadRef and DownloadPhase: a grab entry in flight.
	ActiveDownloadRef *string
	DownloadPhase     commonv1.DownloadPhase
	// PendingGrab is whether status.pendingGrab is set.
	PendingGrab bool
	// SearchInFlight is status.searchDispatch.InFlight().
	SearchInFlight bool
	// Conditions are status.conditions as last written.
	Conditions []metav1.Condition
}

// LeafIdleAtStart reports a non-video leaf whose pass at a manager's start
// would change nothing, so the cache's initial list leaves it to its
// watches: its spec observed, no refresh due, no grab, pending grab or
// search in flight, no retired condition still carried (one pass drops it,
// then the item is idle again), and either Unmonitored while it still is --
// its own flag or its container's -- or Imported while monitored, which for
// a leaf that is never transcoded (design D9) is final: every file on disk
// at the cutoff, nothing to search. A Wanted, Unavailable, Delayed,
// CutoffUnmet or Pending leaf has a timer or work, and an item whose stored
// phase disagrees with its monitor flag is enqueued, since the pass would
// move it.
func LeafIdleAtStart(in LeafIdle) bool {
	if in.ObservedGeneration != in.Generation || in.RefreshDue || in.ActiveDownloadRef != nil ||
		in.DownloadPhase != "" || in.PendingGrab || in.SearchInFlight {
		return false
	}
	for i := range in.Conditions {
		if RetiredLeafConditions[in.Conditions[i].Type] {
			return false
		}
	}
	switch in.Phase {
	case commonv1.LeafPhaseUnmonitored:
		return !in.Monitored
	case commonv1.LeafPhaseImported:
		return in.Monitored
	}
	return false
}
