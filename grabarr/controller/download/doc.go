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

// Package download reconciles Download: it picks a DownloadClient for a
// newly-created Download, waits for that client's engine to report ready,
// pins the choice into status.engine so a later reconcile cannot silently
// migrate a running transfer, and runs a finalizer honouring
// spec.removeDataOnDelete. Design spec §6.3; plan task D2-4. Field manager:
// k8s.ManagerGrabarr only -- see grabarr/status for the full split with
// k8s.ManagerGrabarrEngine and why an over-claim against the engine's set is
// silent rather than a conflict.
//
// # ClientRef selection
//
// Among DownloadClients in the Download's namespace, [pickClient] keeps only
// those whose spec.protocol matches and whose spec.enabled is true (or unset,
// which defaults true), then picks the lowest spec.priority, breaking ties on
// name for determinism. This is the design spec's own summary at §6.3:
// "pick ClientRef (enabled, protocol, lowest priority number) once". The
// plan's task text also says "and category"; DownloadClientSpec has no field
// that selects a client by category -- Categories only maps a media kind to
// an output subdirectory once a client is already chosen (spec §4.4) -- so
// this reading treats "category" as already satisfied by protocol matching
// alone, not as a second selection axis.
//
// spec.clientRef is immutable once set (DownloadSpec's own CEL rule), so the
// pick happens at most once per Download: a later reconcile that finds
// spec.clientRef already populated re-reads that DownloadClient rather than
// picking again.
//
// # The engine pin is a one-way door
//
// Once status.engine is non-empty, reconcileNormal does not recompute it,
// ever, in this task. The plan's own framing is "pin the choice ... so a
// later reconcile cannot silently migrate a running transfer" -- full
// immutability satisfies that outright. Design spec §6.3 additionally
// sketches a reassignment path for when a DownloadClient scales down past an
// already-assigned ordinal and the old engine pod is confirmed gone; that
// nuance is NOT implemented here. It is out of this task's named scope
// ("ClientRef pick, EngineReady wait, status.engine pin, finalizer") and
// would be untestable against a real engine that does not exist until
// D2-5/D2-6 land. A future task extending this reconciler owns it.
//
// The ordinal itself is k8s.HashOrdinal(dc.Spec.Replicas, release.InfoHash,
// release.GUID), matching §6.3's "engine = <client>-<hash(infoHash|guid) mod
// spec.replicas>", computed from the DESIRED replica count rather than the
// ready one so a temporarily-down engine keeps its share (HashOrdinal's own
// doc comment).
//
// # What this task deliberately does not do
//
// The eleven-value DownloadPhase enum is closed and pinned (plan ruling R1):
// catalogarr/controller/rollup/downloadoverlay.go's DownloadOverlay switches
// on all of it, with a default branch (Completed, Seeding, Imported, Failed,
// Blocklisted, Removing) that means "no opinion". This reconciler only ever
// writes Pending and Assigned -- the two rows R1 itself says are "exercised
// by anything real before grabarr lands" -- and NEVER writes Removing during
// the finalizer (see below). It is therefore a strict subset of the closed
// set and cannot diverge from the rollup switch by construction: every value
// this package can produce already has a case in that switch.
//
// It follows that Queued/Downloading/Paused/Completed/Seeding/Imported/
// Failed/Blocklisted are not implemented here. Those all require reading
// engine-owned telemetry (status.stage, status.canBeRemoved, ...) that no
// writer produces until D2-5/D2-6 land (wave 3, after this task); a mapping
// written against fields nothing populates would be untested and a guess
// dressed as an implementation. The plan's own D2-4 section names exactly
// four deliverables -- ClientRef pick, EngineReady wait, status.engine pin,
// finalizer -- and none of them requires that mapping.
//
// This package also does not publish schema.DownloadEvent or
// schema.ImportTask, even though design spec §6.3's condensed prose
// ("Phase=Assigned; evt.download.queued; delete the grab lease on terminal
// phase") and plan ruling R8 (both payload types have zero producers) gesture
// at a producer somewhere in grabarr. The plan's own D2-4 task text does not
// mention either one, R8 assigns neither to a specific task, "delete the grab
// lease" is catalogarr/worker/grab's KV state and out of this directory, and
// schema.ImportTask's own subject -- events.WorkFileImportSubject, which
// importarr/worker/fileimport's ConsumerImportFile actually listens on -- is
// disjoint from the "clustarr.work.catalogarr.import..." subject that type's
// own doc comment still (incorrectly) claims. Wiring the Completed-phase
// producer that would make importarr's already-landed consumer fire is a real
// gap in the end-to-end pipeline, but it belongs with whichever task teaches
// this package about Completed in the first place, not with D2-4.
//
// # The finalizer needs no live engine
//
// grabarr's controller Deployment mounts the same RWX DataDir every engine
// pod does (config/manager/grabarr.yaml; grabarr/controller/downloadclient's
// Reconciler.DataDir does the identical statfs for DiskSpaceOK). That means
// spec.removeDataOnDelete can be honoured with a direct
// fsops.SafeRemove(DataDir, status.outputPath) from this controller, with no
// need to coordinate with a live download.Client the controller process does
// not hold -- engine roles hold those, per grabarr/run.go's role split.
// status.outputPath is engine-owned telemetry (k8s.ManagerGrabarrEngine) and
// is empty on every Download this task's own tests can produce, since no
// engine exists yet to set it; the removal branch is exercised by a test that
// plants the field directly, standing in for the engine that will really set
// it once D2-5/D2-6 land.
//
// The finalizer does not set status.phase=Removing. Every phase this
// reconciler is scoped to falls on DownloadOverlay's default branch either
// way (Removing included), so the omission has no observable effect on the
// rollup, and asserting "the engine is tearing the transfer down"
// (DownloadPhaseRemoving's own doc comment) would describe a process this
// controller is not actually running.
//
// # RBAC markers are package-level
//
// See grabarr/controller/downloadclient/doc.go for why: controller-gen only
// collects +kubebuilder:rbac from a comment group that is not attached to a
// declaration. grabarr/status/doc.go already grants downloads and
// downloads/status get;list;watch;update;patch for this controller; the
// verbs are restated here too, because a marker states what the package it
// sits in does, not what is already granted somewhere else in the tree.
// config/rbac/role.yaml and the chart's copy are NOT regenerated by this
// task -- task D2-8 runs `make manifests` once every controller in the phase
// exists, per its own task text.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package download
