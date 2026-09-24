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

// Package grab is the second half of spec §8.2: it turns an already-approved
// release into a Download, holding it back for a DelayProfile's window first
// when the profile asks for one.
//
// Two entry points matter.
//
// Decide is the synchronous one. A caller that has already run
// pkg/decision.Evaluate/Rank and resolved a quality.Profile and a
// catalogv1alpha1.DelayProfileSpec hands the winning release to Decide, which
// either grabs it immediately (no delay configured, or a bypass fires) or
// CAS-keep-bests it into the clustarr-pending bucket, schedules a GrabTask on
// clustarr.work.catalogarr.grab.normal.<mediaKey> for firstSeen+delay, and
// records status.pendingGrab. Both the search worker (Task C8) and the RSS
// matcher call it.
//
// Handler is the asynchronous one: the catalogarr-grab consumer. When the
// scheduled GrabTask is finally delivered it re-reads the pending entry --
// which may by then hold a better release than the one that created it -- and
// performs the grab.
//
// # Field-manager split
//
// This package writes status under k8s.ManagerCatalogarrGrab and applies
// exactly three fields: status.pendingGrab, status.lastSearchedAt and
// status.searchAttempts. It never writes status.phase. Recomputing
// Phase=Delayed/Downloading from those fields belongs to the Movie and Episode
// reconcilers under k8s.ManagerCatalogarr; this package's writes are what wake
// them, via the status.pendingGrab arm of their own-object predicates.
//
// It does not write status.activeDownloadRef either (gap-fix ruling R-5).
// That field has one writer, the item's reconciler, which derives it from the
// item's non-terminal Download; the Download this package creates is what the
// reconciler finds. The double-grab guard does not read the ref: it lists the
// Downloads covering the item itself (package downloads holds the notion of
// "covering" and "terminal" that both sides use).
//
// The manager name is its own rather than the shared catalogarr-worker
// precisely because server-side apply replaces a manager's whole ownership
// set on every apply. While this path and the metadata gateway shared one
// name, each one's apply deleted the other's fields: a grab dropped the
// movie's cached metadata, and a metadata refresh dropped
// status.pendingGrab, taking a delayed item out of Phase=Delayed back to
// Wanted.
//
// Within this package the same rule still applies to its own three fields,
// so every PatchStatus here is a complete declaration of what this manager
// owns on that object. updateWorkerStatus is the single place that assembles
// it, and every caller goes through it, so a failure path cannot build a
// partial status and release the rest. It is also a compare-and-swap -- the
// declare is conditional on the resourceVersion it was read at -- because
// every replica runs these consumers, and a declaration built from a stale
// read would roll back another replica's write rather than release it.
//
// # Registration (Task C12)
//
// Nothing in this package registers itself. app/catalog/run.go's
// setupQueueWorkers makes this call, with the topology it installed:
//
//	grabHandler := grab.NewHandler(grab.Deps{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Bus: bus})
//	grabHandler.Topology = &topo // o.BusTopology()
//	if err := grabHandler.SetupWithManager(mgr, bus); err != nil {
//		return fmt.Errorf("catalogarr: subscribe grab: %w", err)
//	}
//
// SetupWithManager adds a manager.Runnable that subscribes on manager start
// and detaches on manager stop, so the subscription's lifetime is the
// manager's rather than context.Background()'s.
package grab
