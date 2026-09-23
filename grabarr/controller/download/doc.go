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
// migrate a running transfer, advances status.phase from engine-owned
// telemetry once pinned, publishes the file-import work item once content
// is complete on disk, and runs a finalizer honouring
// spec.removeDataOnDelete. Design spec §6.3; plan tasks D2-4 and D2-8a.
// Field manager: k8s.ManagerGrabarr only -- see grabarr/status for the full
// split with k8s.ManagerGrabarrEngine and why an over-claim against the
// engine's set is silent rather than a conflict.
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
// # What D2-4 deliberately did not do, and what D2-8a closed
//
// The eleven-value DownloadPhase enum is closed and pinned (plan ruling R1):
// catalogarr/controller/rollup/downloadoverlay.go's DownloadOverlay switches
// on all of it, with a default branch (Completed, Seeding, Imported, Failed,
// Blocklisted, Removing) that means "no opinion". D2-4 (43845da) only ever
// wrote Pending and Assigned, because the mapping a further phase needed
// requires engine-owned telemetry (status.stage, status.isEncrypted, ...)
// that no writer produced until D2-5/D2-6 landed (wave 3, after D2-4) --
// writing it earlier would have been a guess dressed as an implementation,
// untestable against fields nothing populated.
//
// D2-8a is that further task. [derivePhase] (phase.go) reads status.stage,
// status.isEncrypted, status.import and the blocklist label, and
// advancePhase (controller.go) drives status.phase through
// Assigned -> Queued -> Downloading -> Completed/Seeding -> Imported, plus
// Paused and Failed. It remains a strict subset of the closed set and
// cannot diverge from the rollup switch by construction -- phase.go's own
// doc comment gives the full accounting, verified against
// downloadoverlay.go's source. Removing is still never written, by the
// finalizer or anywhere else (see below, unchanged from D2-4).
//
// derivePhase does not reach every DownloadFailureReason: only encrypted is
// telemetry-derivable without a live download.Client, which this package
// still does not hold (see "The finalizer needs no live engine" below).
// stalled, diskFull, writeError, timeout, missingArticles and manual remain
// unimplemented -- phase.go explains why each would need either a live
// Client or an unsourced inactivity-threshold judgment call, which this
// task declined to make up rather than guess at. SeedGoalMet is the one
// condition grabarr/status.ControllerFields lists (its own doc comment:
// "the reconciler derives all five on every pass") that advancePhase does
// not compute, for the same reason: status.canBeRemoved conflates "seed
// goal met" with "import finished" and nothing else in the available
// telemetry distinguishes the two.
//
// advancePhase also now publishes schema.ImportTask to
// events.WorkFileImportSubject(<download-uid>) the first reconcile that
// observes the content complete on disk (Completed or Seeding) --
// design spec §6.3's condensed prose ("Phase=Assigned; evt.download.queued;
// delete the grab lease on terminal phase") and plan ruling R8 (the payload
// type had zero producers) both gestured at this producer without D2-4
// claiming it. It is the one importarr/worker/fileimport's ConsumerImportFile
// (D2-7) has been waiting on since it landed; see controller.go's
// publishImportTask and advancePhase doc comments for the exactly-once
// discipline (a Downloaded-condition gate, backed by a deterministic
// Envelope.ID for the broker's own dedup window as a second, independent
// layer). schema.DownloadEvent and schema.DownloadProgress remain
// unpublished by this package -- "delete the grab lease" is
// catalogarr/worker/grab's KV state and out of this directory regardless --
// so §6.3's evt.download.queued notification and the 1Hz progress stream
// are still a real gap for whichever task takes them next.
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
// The finalizer does not set status.phase=Removing (unchanged by D2-8a,
// which owns advancePhase, not reconcileDelete). Removing itself falls on
// DownloadOverlay's default branch regardless of who writes it, so the
// omission has no observable effect on the rollup either way, and asserting
// "the engine is tearing the transfer down" (DownloadPhaseRemoving's own
// doc comment) would describe a process this controller is not actually
// running.
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
