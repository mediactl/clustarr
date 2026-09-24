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

// Package engine holds what grabarr's two transfer engines
// (app/grab/engine/torrent, app/grab/engine/usenet) share with each other and
// with the Download controller: the engine finalizer that orders a
// Download's teardown (gap-fix ruling R-6), and the age bookkeeping both
// orphan reapers use.
//
// # The teardown protocol (R-6)
//
// A Download has two parties that hold something on its behalf: the engine
// replica running its transfer (open files, peer connections, a persisted
// re-attach descriptor or scratch manifest) and the Download controller,
// whose removeDataOnDelete finalizer deletes status.outputPath from the
// shared data volume. Before R-6 the controller deleted the files and let
// the object go without waiting for any engine, so an engine could still be
// writing into -- or seeding from -- files already unlinked, and a Download
// deleted while its engine was down left the transfer running with no object
// to say so.
//
// The order is now explicit:
//
//  1. Each engine adds [Finalizer] to every Download labelled for it before
//     it adds the transfer, so no transfer exists without the finalizer.
//  2. When the Download is deleted, the engine removes the transfer from its
//     client (and its own persisted state), then drops [Finalizer].
//  3. The controller runs its removeDataOnDelete finalizer only once
//     [Finalizer] is gone -- or, when the engine is gone (its DownloadClient
//     deleted, its engine not ready, or its ordinal scaled away), after a
//     bounded timeout, at which point the controller drops [Finalizer] on
//     the engine's behalf. The orphan reaper then clears the transfer if
//     that engine ever comes back.
//
// Both halves use pkg/k8s.EnsureFinalizer/RemoveFinalizer, which write with
// Update under optimistic concurrency, so the two finalizers on one object
// never overwrite each other; a conflict is a retry.
package engine

import (
	"sync"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// Stopped reports whether an engine must stop dl's transfer rather than run
// it: the controller has failed it (phase Failed) or blocklisted it (phase
// Blocklisted), or it carries the blocklist label the controller is about to
// act on. Both engines remove a stopped Download's transfer and never add
// one, so a release that failed -- or that an operator blocklisted before
// its engine got to it -- is not fetched again.
//
// An Imported Download is never stopped, label or not: the controller keeps
// Imported sticky over the label (app/grab/controller/download's
// derivePhase), and its transfer is governed by spec.removeOnImport.
func Stopped(dl *downloadv1alpha1.Download) bool {
	switch dl.Status.Phase {
	case downloadv1alpha1.DownloadPhaseFailed, downloadv1alpha1.DownloadPhaseBlocklisted:
		return true
	case downloadv1alpha1.DownloadPhaseImported:
		return false
	}
	return dl.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue
}

// Finalizer is the finalizer every engine adds to a Download it owns. It is
// distinct from the Download controller's own finalizer
// (k8s.FinalizerFor(Download) = "download.clustarr.io/download") because the
// two guard different things: this one the transfer, that one the data. See
// the package doc for the ordering between them.
const Finalizer = "download.clustarr.io/engine"

// OrphanClock is the bookkeeping behind both engines' orphan reapers: it
// decides when a transfer the client holds, but no Download this replica
// knows claims, is old enough to be treated as an orphan rather than as a
// transfer added moments ago whose Download has not recorded its id yet.
//
// # Age, not first sighting
//
// The age is the transfer's own [download.Item.AddedAt], which the client
// carries across restarts. The reapers used to count from the first pass
// that saw the transfer unmatched, held only in memory, so every engine
// restart re-extended every orphan's grace period and a crash-looping engine
// could defer reaping indefinitely (the Phase D2 carried item). With the
// real age, a transfer orphaned while its engine was down is due on the
// first pass after the restart.
//
// Only when the client does not know a transfer's age -- AddedAt is zero,
// as for state persisted before the field existed -- does the clock fall
// back to its own first unmatched sighting, which is never less safe, only
// less prompt.
//
// A matched transfer is never due, and a fallback clock restarts from the
// next unmatched sighting once it has been matched, so a transient miss
// does not carry a stale sighting forward.
//
// The zero value is ready to use and is safe for concurrent use.
type OrphanClock struct {
	mu        sync.Mutex
	firstSeen map[string]time.Time
}

// Due records one pass over items and returns the ids that have no entry in
// known and are at least grace old at now.
func (o *OrphanClock) Due(items []download.Item, known map[string]struct{}, now time.Time, grace time.Duration) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.firstSeen == nil {
		o.firstSeen = make(map[string]time.Time)
	}

	seen := make(map[string]struct{}, len(items))
	var due []string
	for _, item := range items {
		seen[item.ID] = struct{}{}
		if _, ok := known[item.ID]; ok {
			delete(o.firstSeen, item.ID)
			continue
		}

		since := item.AddedAt
		if since.IsZero() {
			first, tracked := o.firstSeen[item.ID]
			if !tracked {
				o.firstSeen[item.ID] = now
				continue
			}
			since = first
		}
		if now.Sub(since) >= grace {
			due = append(due, item.ID)
		}
	}

	// Drop bookkeeping for ids the client no longer reports at all, so the
	// map does not grow across a long-lived engine process.
	for id := range o.firstSeen {
		if _, ok := seen[id]; !ok {
			delete(o.firstSeen, id)
		}
	}
	return due
}

// Forget drops id's fallback sighting, after it has been reaped.
func (o *OrphanClock) Forget(id string) {
	o.mu.Lock()
	delete(o.firstSeen, id)
	o.mu.Unlock()
}

// Reset drops every fallback sighting, for a pass on which the client holds
// no transfers at all.
func (o *OrphanClock) Reset() {
	o.mu.Lock()
	o.firstSeen = nil
	o.mu.Unlock()
}
