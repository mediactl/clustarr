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

// Package rescan is importarr's library-rescan work consumer: the
// clustarr.work.importarr.scan.* handler that walks a RootFolder, classifies
// what it finds with pkg/fsops, parses each media file with pkg/release,
// attributes it to a catalog item, and creates or updates the Movie and the
// MediaFile that record it (amendment §A1.3, §A1.5, §A1.6; §16 M1).
//
// The scanner never guesses. A file that cannot be attributed with
// confidence is recorded as an [UnmatchedFile] with a reason, never turned
// into a speculative catalog item -- [MatchMovie] is the pure function that
// decision lives in, so the "must not match" cases are testable without a
// cluster.
//
// # The status split
//
// This worker is the sole writer of MediaFileSpec: the observed path, size
// and mtime, plus the quality, revision, release type, release group,
// edition and languages frozen at import (spec §8.4). It never writes
// MediaFileStatus, which catalogarr owns in full and populates by probing
// (spec §8.5), and it never re-applies a MediaFile whose spec.original is
// already false -- catalogarr has taken spec.sizeBytes, spec.modTime and
// spec.original over after a transcode swap, and k8s.Apply's force-ownership
// would silently reclaim them. Every write is made under
// k8s.ManagerImportarr.
//
// The worker also never writes LibraryScan.status: the LibraryScan
// controller is its single writer. Progress instead flows through a
// [Progress] checkpoint in the clustarr-progress bucket, written roughly
// every [checkpointInterval] and polled by the controller, with in-progress
// acks roughly every [heartbeatInterval] so a multi-minute walk outlives
// ConsumerImportScan's 60s AckWait.
//
// # Registration
//
// Nothing here registers itself. Task C12 wires it into importarr's
// setupWorkers with exactly:
//
//	if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
//	        return fmt.Errorf("importarr: index mediafile path: %w", err)
//	}
//	spec, ok := o.BusTopology().Consumer(events.ConsumerImportScan)
//	if !ok {
//	        return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportScan)
//	}
//	worker := rescan.NewWorker(mgr.GetClient(), bus)
//
// k8s.EveryReplica, not manager.RunnableFunc: the latter has no
// NeedLeaderElection method, so controller-runtime puts it behind the leader
// lease on any service that elects.
//
//	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
//	        stop, err := bus.Subscribe(ctx, spec.Subscription(), worker.Handle)
//	        if err != nil {
//	                return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportScan, err)
//	        }
//	        <-ctx.Done()
//	        stop()
//	        return nil
//	})); err != nil {
//	        return err
//	}
//
// IndexMediaFileByPath must run before the manager starts: it registers the
// spec.path field index the incremental fingerprint check reads.
package rescan
