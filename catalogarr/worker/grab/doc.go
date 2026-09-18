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
// This package writes status under k8s.ManagerCatalogarrWorker and applies
// exactly three fields: status.activeDownloadRef, status.pendingGrab and
// (never yet, but reserved by the same manager) the search bookkeeping. It
// never writes status.phase. Recomputing Phase=Delayed/Downloading from those
// fields belongs to the Movie and Episode reconcilers under
// k8s.ManagerCatalogarr; this package's writes are what wake them, via the
// status.pendingGrab arm of their own-object predicates.
//
// Server-side apply replaces a manager's whole ownership set on every apply,
// so every PatchStatus here is a complete declaration of what this manager
// owns on that object. patchStatus is the single place that assembles it, and
// every caller goes through it precisely so a failure path cannot build a
// partial status and release the rest.
//
// # Registration (Task C12)
//
// Nothing in this package registers itself. catalogarr/run.go's setupWorkers
// makes exactly this call:
//
//	grabHandler := grab.NewHandler(grab.Deps{Client: mgr.GetClient(), Bus: bus})
//	if err := grabHandler.SetupWithManager(mgr, bus); err != nil {
//		return fmt.Errorf("catalogarr: subscribe grab: %w", err)
//	}
//
// SetupWithManager adds a manager.Runnable that subscribes on manager start
// and detaches on manager stop, so the subscription's lifetime is the
// manager's rather than context.Background()'s.
package grab
