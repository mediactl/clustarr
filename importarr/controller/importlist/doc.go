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

// Package importlist is the ImportList controller: it schedules one
// clustarr.work.importarr.list.<name> sync task per spec.refreshInterval
// tick (clamped up to the provider's own floor -- schedule.go), drives
// Trakt's device-code authorization handshake (auth.go), and is the sole
// writer of ImportList.status (amendment §A1.3, §A1.6, design spec §8.7;
// task G1-3).
//
// It never fetches a list itself. importarr/worker/importlist does that,
// on the schedule this controller publishes -- see that package's doc
// comment for why: this controller polls that worker's checkpoint
// (importlist.ResultKey/Result, in the clustarr-progress bucket) and
// projects it into status, the same split
// importarr/controller/libraryscan runs against
// importarr/worker/rescan's Progress checkpoint for LibraryScan.
//
// # Why a worker on a queue, not a controller-driven requeue alone
//
// Task G1-3's brief asks which of the two pkg/events already declares for
// import lists: grepping subjects.go and topology.go turns up
// events.ConsumerImportList ("importarr-list"), events.FilterImportList
// ("clustarr.work.importarr.list.>") and events.WorkListSubject, all wired
// into StreamWorkImportarr's ConsumerSpec already -- amendment §A1.6 names
// exactly this subject in its process-topology table. So the sync itself
// runs on that queue, in importarr/worker/importlist; this controller's
// role is the "plus a scheduled sync" half: deciding WHEN to enqueue one,
// via RequeueAfter, the same self-timed mechanism
// importarr/controller/rootfolderschedule uses for LibraryScan ticks.
//
// # Trakt device-code flow
//
// spec.trakt requires spec.secretRef to hold the Trakt application's
// clientID and clientSecret (the app credentials registered with Trakt,
// not a user token). This controller drives
// pkg/importlist/trakt.DeviceFlow -- Start to mint a user code, Poll on a
// RequeueAfter cadence matching Trakt's own requested interval, Refresh
// transparently inside the worker's Fetch call -- and surfaces
// status.auth.{userCode,verificationURL,expiresAt} so the UI (task G3-4)
// can show the user what to enter and where. The resulting token pair (and
// the in-flight device code between Start and Poll) is persisted in an
// owned Secret via importlist.SecretTokenStore, which survives a pod
// restart the way pkg/importlist.MemoryTokenStore does not.
//
// # Field manager
//
// Every status write in this package is under k8s.ManagerImportarr, whose
// own doc comment already names ImportList, ImportExclusion and
// LibraryScan as what it owns. The worker never writes ImportList.status
// (see this package's poll-and-project split above), so there is exactly
// one writer of the subresource, from exactly one process.
//
// # Registration
//
// Nothing here registers itself. Task G1-5 wires it into importarr's
// setupControllers, alongside libraryscan, rootfolderschedule and
// importexclusion already there, with exactly the shape those three use:
//
//	if err := (&importlist.Reconciler{
//	        Client: mgr.GetClient(),
//	        Bus:    bus,
//	}).SetupWithManager(mgr); err != nil {
//	        return fmt.Errorf("importarr: importlist: %w", err)
//	}
package importlist
