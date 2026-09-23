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

// Package rootfolderschedule turns RootFolder.spec.scanSchedule into
// LibraryScan objects, one per cron tick (amendment §A1.3, §A1.4; §16 M1).
//
// It is the second controller watching RootFolder, and the one-writer rule
// still holds: catalogarr's RootFolder reconciler owns RootFolder.status and
// this package never touches the status subresource. What it does write is
// one annotation, [AnnotationLastTick], on the RootFolder's metadata, under
// its own field manager.
//
// # Why the last tick is an annotation
//
// The obvious implementation -- take the newest existing LibraryScan's
// creationTimestamp as "when did we last fire" -- has a nasty failure mode: a
// LibraryScan deletes itself once spec.ttlSecondsAfterFinished elapses
// (an hour by default), so an hour after each scan the controller sees no
// scans at all, concludes it has never fired, and fires again. A root folder
// on a nightly schedule would be walked every hour instead. The annotation is
// the smallest durable record that survives the scan it describes, and it is
// visible in `kubectl get rootfolder -o yaml` rather than hidden in a bucket.
//
// Ticks are not backfilled. A controller that was down across a tick
// schedules the next one rather than firing immediately, the same choice
// Kubernetes CronJob makes with a missed start: a library walk that fires
// late is rarely more useful than the next one, and a restart loop that
// backfilled would walk the library continuously. The single exception is a
// RootFolder the controller has never seen before, which is scanned once on
// adoption and then stamped -- adding a root folder to the cluster is
// precisely the moment an operator wants its contents indexed.
//
// # One walk at a time
//
// A due tick does not fire while an earlier LibraryScan of the same root
// folder is still Pending or Running, whoever created it: two walks of one
// tree do the same work twice and race each other's MediaFile writes. The
// tick is deferred, not skipped -- Kubernetes CronJob's concurrencyPolicy:
// Forbid -- so it fires once the earlier scan settles, collapsed like any
// late tick to the most recent one due. A schedule shorter than a walk
// walks back to back.
//
// # Registration
//
// Nothing here registers itself. Task C12 wires it into importarr's
// setupControllers with exactly:
//
//	if err := (&rootfolderschedule.Reconciler{
//	        Client:   mgr.GetClient(),
//	        Recorder: mgr.GetEventRecorder("rootfolderschedule"),
//	        Clock:    time.Now,
//	}).SetupWithManager(mgr); err != nil {
//	        return fmt.Errorf("importarr: rootfolderschedule: %w", err)
//	}
//
// Recorder is a k8s.io/client-go/tools/events.EventRecorder, which writes
// events.k8s.io/v1 -- the one recorder convention in this tree, and the
// reason the package's events RBAC marker names events.k8s.io rather than the
// core group. The deprecated mgr.GetEventRecorderFor is not used anywhere.
package rootfolderschedule
