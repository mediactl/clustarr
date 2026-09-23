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

// Package libraryscan implements the LibraryScan controller: the single
// writer of LibraryScan.status and the only thing that turns a LibraryScan
// into work (amendment §A1.4, §A1.6; §16 M1).
//
// A scan moves through three steps, dispatched on status.phase:
//
//   - start: resolve the RootFolder, publish exactly one schema.ScanTask on
//     clustarr.work.importarr.scan.<rootfolder>, and record Running;
//   - poll: read the rescan worker's clustarr-progress checkpoint and
//     aggregate it into status, capping unmatched at the CRD's 200 newest.
//     The aggregate never lowers a counter (a redelivery whose checkpoint
//     expired restarts its tally), the Ready message carries the worker's
//     full breakdown (rescan.Progress.Summary), and a Running scan fails
//     when its task is dead-lettered or no checkpoint has arrived for
//     noProgressTimeout since the last one;
//   - expire: delete the scan once spec.ttlSecondsAfterFinished has elapsed
//     since it settled.
//
// # One task per scan, not one per directory
//
// Amendment §A1.6 says a large scan is "chunked by directory". This
// controller publishes one task per LibraryScan instead, and the worker
// heartbeats and checkpoints through it. The reason is the CRD:
// status.unmatched is a plain list with no listType=map, so concurrent chunk
// writers would clobber each other's entries, and only this controller may
// write LibraryScan.status at all -- real chunking needs either a new capped
// per-chunk field or a KV.Watch aggregator. Intra-scan directory fan-out is
// follow-up work, not a silent omission. ConsumerImportScan's MaxAckPending
// of 4 still does useful work: four different scans can be in flight at once.
//
// # Registration
//
// Nothing here registers itself. Task C12 wires it into importarr's
// setupControllers with exactly:
//
//	if err := (&libraryscan.Reconciler{
//	        Client: mgr.GetClient(),
//	        Bus:    bus,
//	        Clock:  time.Now,
//	}).SetupWithManager(mgr); err != nil {
//	        return fmt.Errorf("importarr: libraryscan: %w", err)
//	}
//
// setupControllers therefore needs the bus in scope; in Run it already is,
// from the k8s.ConnectBus call above the setupControllers call site.
package libraryscan
