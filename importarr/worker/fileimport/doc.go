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

// Package fileimport is importarr's completed-download import worker: the
// clustarr.work.importarr.fileimport.<download> handler that turns a
// finished Download into one or more MediaFiles under a RootFolder
// (amendment §A1.6; spec §8.4, adjusted for the amendment's relocation of
// the importer out of catalogarr -- see [FieldManager] and this package's
// own status-ownership note below).
//
// # The status split
//
// This worker creates the MediaFile and is the sole writer of
// MediaFileSpec: the observed destination path, size and mtime, plus the
// quality, revision, format score, matched formats and release type frozen
// at import (spec §8.4, CLAUDE.md's invariant). It never writes any part of
// MediaFileStatus, which catalogarr owns in full and populates by probing
// (spec §8.5) -- this worker does not call pkg/mediainfo at all, on
// purpose.
//
// It is also, uniquely, a cross-group status writer: it applies
// Download.status.import under k8s.ManagerImportarr, the one field manager
// on a download.clustarr.io object that importarr, not grabarr, owns. See
// k8s.ManagerImportarr's doc comment for why that write settled here rather
// than under catalogarr (design spec §8.4's original assignment, superseded
// by amendment-1) and why it uses the bare controller-manager name rather
// than [FieldManager] (this package never shares a manager name with
// another importarr writer on the SAME object type, so the collision
// [FieldManager] guards against on MediaFile does not apply to Download).
//
// The scanner-never-guesses rule (amendment §A1.5) applies here too, in its
// file-import shape: a file this worker cannot confidently attribute to the
// Download's target, or cannot parse, is never turned into a speculative
// MediaFile. It is recorded in Download.status.import.rejections with a
// reason instead -- the file-import analogue of LibraryScan.status.unmatched,
// since a Download has no LibraryScan to report through.
//
// # Scope
//
// Only Target.Kind == commonv1.MediaKindMovie is implemented. A Download
// whose target is any other kind is recorded as
// [downloadv1alpha1.ImportPhaseIgnored] with a clear reason rather than
// attempted: series/episode packs, and every non-video kind, are later
// milestone work (mirroring importarr/worker/rescan's identical scope cut
// for RootFolder kinds).
//
// # Registration
//
// Nothing here registers itself. Task D2-8 wires it into importarr-worker's
// setup with exactly:
//
//	if err := fileimport.IndexMediaFileByTarget(ctx, mgr); err != nil {
//	        return fmt.Errorf("importarr: index mediafile target: %w", err)
//	}
//	spec, ok := o.BusTopology().Consumer(events.ConsumerImportFile)
//	if !ok {
//	        return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportFile)
//	}
//	worker := fileimport.NewWorker(mgr.GetClient(), bus)
//
//	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
//	        stop, err := bus.Subscribe(ctx, spec.Subscription(), worker.Handle)
//	        if err != nil {
//	                return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportFile, err)
//	        }
//	        <-ctx.Done()
//	        stop()
//	        return nil
//	})); err != nil {
//	        return err
//	}
//
// k8s.EveryReplica, not manager.RunnableFunc, for the same reason
// importarr/worker/rescan uses it: manager.RunnableFunc has no
// NeedLeaderElection method, so controller-runtime would put this behind the
// leader lease on any service that elects, and a completed-download import
// must not wait for leadership.
//
// IndexMediaFileByTarget must run before the manager starts: it registers
// the field index this worker uses to find a target's existing MediaFile.
package fileimport
