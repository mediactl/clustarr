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

// DownloadNonTerminal reports whether dl can still deliver content to the
// catalog item it targets, which is what status.activeDownloadRef means
// (gap-fix ruling R-5). A Download is terminal once it is Imported, Failed,
// Blocklisted or Removing, or once it carries a deletion timestamp: grabarr
// never sets Removing itself (its finalizer tears the transfer down without
// a phase change), so a Download on its way out would otherwise read as
// active for as long as its engine takes to let go of it.
//
// Completed and Seeding are deliberately NOT terminal. The content is on
// disk and waiting for importarr, and derivePhase makes Imported sticky
// over Seeding, so a Seeding Download has not been imported yet. Clearing
// the ref there would tell every reader -- the grab path's double-grab
// guard above all -- that nothing is working on an item whose file is one
// import away.
//
// The set is the same one catalogarr/worker/search/blocklist.go's
// isTerminal uses for the live queue, so the item's ref and the search
// worker's "is this target already queued" can never disagree about one
// Download. DownloadOverlay is built on it too, so the item's phase reads
// Downloading exactly while this reports true.
func DownloadNonTerminal(dl *downloadv1alpha1.Download) bool {
	if dl == nil || dl.DeletionTimestamp != nil {
		return false
	}
	switch dl.Status.Phase {
	case downloadv1alpha1.DownloadPhaseImported,
		downloadv1alpha1.DownloadPhaseFailed,
		downloadv1alpha1.DownloadPhaseBlocklisted,
		downloadv1alpha1.DownloadPhaseRemoving:
		return false
	default:
		return true
	}
}

// ActiveDownload picks the Download a catalog item's status.activeDownloadRef
// names, out of the Downloads whose spec.target covers that item: the
// oldest one that owns reports true for and that is still non-terminal, with
// the name breaking a creation-time tie. nil means none.
//
// owns is the caller's ownership test -- the item's own UID for a movie or a
// single episode, the parent Series' UID for an episode inside a season pack
// -- so a Download left behind by a deleted-and-recreated item of the same
// name, which the garbage collector has not reached yet, is never adopted.
//
// The oldest wins because two live Downloads for one item is a double grab,
// and the first one is the grab the second should have been refused against.
// The choice is a function of the Downloads alone, not of the ref already on
// the item, so the ref is derived level-style: whatever happened in between,
// the next reconcile computes the same answer from the same Downloads.
func ActiveDownload(items []downloadv1alpha1.Download, owns func(*downloadv1alpha1.Download) bool) *downloadv1alpha1.Download {
	var best *downloadv1alpha1.Download
	for i := range items {
		dl := &items[i]
		if !DownloadNonTerminal(dl) || (owns != nil && !owns(dl)) {
			continue
		}
		if best == nil || olderThan(dl, best) {
			best = dl
		}
	}
	return best
}

func olderThan(a, b *downloadv1alpha1.Download) bool {
	at, bt := a.CreationTimestamp.Time, b.CreationTimestamp.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return a.Name < b.Name
}
