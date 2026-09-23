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

package download

import (
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// phaseResult is what derivePhase computes from a Download's own history
// (spec.paused, the blocklist label, status.import) and its engine-owned
// telemetry (status.stage, status.isEncrypted). failureReason is meaningful
// only when phase is DownloadPhaseFailed.
type phaseResult struct {
	phase         downloadv1alpha1.DownloadPhase
	failureReason downloadv1alpha1.DownloadFailureReason
}

// derivePhase computes dl's lifecycle phase (task D2-8a). It is called only
// once status.engine is pinned (controller.go's "one-way door"), so
// DownloadPhaseAssigned is always a legal answer, never a regression, for a
// Download this function has not seen telemetry for yet.
//
// # Why this is a strict subset of DownloadOverlay's switch
//
// Every value derivePhase can produce -- Assigned, Queued, Downloading,
// Paused, Completed, Seeding, Imported, Failed, Blocklisted -- already has a
// case in catalogarr/controller/rollup/downloadoverlay.go's DownloadOverlay
// switch, verified against that source rather than assumed: Pending and
// Assigned/Queued/Downloading/Paused are named explicitly there, and
// Completed, Seeding, Imported, Failed, Blocklisted and Removing all fall
// through its default branch ("no opinion"). derivePhase never returns
// Pending (that stays controller.go's pre-assignment applyStatus calls) or
// Removing (the finalizer does not set it -- see doc.go's "The finalizer
// needs no live engine", unchanged by this task), so the mapping cannot
// diverge from that switch by construction. Plan ruling R1 is satisfied
// without any change to downloadoverlay.go itself.
//
// # What "the item's reported status" (plan task text, spec §7) means here
//
// pkg/download.Item.Status -- the engine's own six-way
// Queued/Paused/Downloading/Completed/Failed/Warning view of a transfer --
// is deliberately never persisted to Download.status: pkg/download/status.go's
// ApplyStatus omits it on purpose, because status.phase is
// k8s.ManagerGrabarr's alone to write and an engine that could report a
// ready-made phase could move the object through the pinned enum from
// inside a transfer loop. This controller also holds no live
// download.Client of its own -- doc.go's "The finalizer needs no live
// engine" says the same thing about the finalizer, and grabarr/run.go's
// role split is why: engine roles hold the Client, the controller role does
// not. So derivePhase approximates "the item's reported status" from what
// IS persisted -- status.stage, status.isEncrypted, status.import and the
// blocklist label -- rather than from a live Client.Get.
//
// That is a narrower signal than a live Item.Status/Item.FailureReason
// would give, and the gap is deliberate rather than an oversight:
// DownloadFailureReason's diskFull, writeError, timeout, missingArticles and
// manual members have no telemetry-only proxy here and are NOT produced by
// this function -- only encrypted is, because status.isEncrypted is an
// unambiguous persisted boolean. stalled is not attempted either: deriving
// it would mean picking an inactivity threshold with no source in the spec
// or an existing constant (SeedCriteria.InactiveTime governs when a torrent
// stops SEEDING for lack of peers -- a different concept from a stalled
// in-progress transfer), which is exactly the kind of unsourced judgment
// call the plan's own carried notes ask to flag rather than guess at. A
// future task extending this reconciler, or one that gives it a live
// Client, owns the remaining failure reasons.
//
// # progressPercent
//
// status.progressPercent does not appear below even though the plan task
// text names it as an input. It does not discriminate between any two phase
// values in the closed enum -- DownloadPhaseDownloading covers the whole
// 1-99% range as one phase -- so branching on it would not change any
// answer this function gives. It stays purely informational (already its
// own printer column on the CRD) rather than a phase input.
func derivePhase(dl *downloadv1alpha1.Download) phaseResult {
	// Imported is sticky and checked first: once importarr's file-import
	// worker (a different service, a different field manager,
	// k8s.ManagerImportarr) has reported success, nothing engine-telemetry
	// shaped may revisit that verdict -- not a stale isEncrypted flag, not a
	// later blocklist label, not the engine's own cleanup after
	// MarkImported changes its stage.
	if dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported {
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseImported}
	}

	// Blocklisting is an external policy decision -- download_types.go's own
	// LabelBlocklisted doc comment: "the blocklist IS the set of Downloads
	// carrying this label" -- not a transfer outcome, so it overrides
	// telemetry the same way Imported does. Nothing in the tree sets this
	// label yet (grep finds only readers: downloadclient.BlocklistSweeper
	// and catalogarr/worker/search/blocklist.go), so this branch is
	// currently unreached in production; it is implemented because the
	// label and the BlocklistedUntil sweep machinery already exist and cost
	// nothing extra to honour correctly once a future writer lands.
	if dl.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue {
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseBlocklisted}
	}

	if dl.Status.IsEncrypted {
		return phaseResult{
			phase:         downloadv1alpha1.DownloadPhaseFailed,
			failureReason: downloadv1alpha1.DownloadFailureEncrypted,
		}
	}

	// Paused: "by spec.paused or by a health action" (DownloadPhasePaused's
	// own doc comment). Only the spec.paused half is implemented -- there is
	// no health-action writer anywhere in this tree yet either.
	if dl.Spec.Paused {
		return phaseResult{phase: downloadv1alpha1.DownloadPhasePaused}
	}

	switch dl.Status.Stage {
	case "":
		// ApplyStatus omits Stage while the engine "has not decided on a
		// stage yet" (its own doc comment) -- the CRD enum rejects "", so
		// this is the shape before any telemetry has arrived, not an
		// outcome. Nothing has changed since the pin.
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseAssigned}
	case downloadv1alpha1.DownloadStageFetchingMetadata:
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseQueued}
	case downloadv1alpha1.DownloadStageTransferring,
		downloadv1alpha1.DownloadStageVerifying,
		downloadv1alpha1.DownloadStageRepairing,
		downloadv1alpha1.DownloadStageExtracting,
		downloadv1alpha1.DownloadStagePublishing:
		// Post-transfer processing (verify/repair/extract/publish) is not
		// yet "on disk and ready to import" -- DownloadPhaseCompleted's own
		// doc comment -- so it groups with the active-transfer phase, the
		// same "actively working on it" bucket
		// catalogarr/controller/rollup/downloadoverlay.go's own comment uses
		// for Assigned/Queued/Downloading/Paused.
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}
	case downloadv1alpha1.DownloadStageSeeding:
		// The content is already published -- DownloadPhaseSeeding's own doc
		// comment: "imported or importable and the torrent is seeding".
		// Usenet never reports this stage; it has no seeding concept.
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseSeeding}
	case downloadv1alpha1.DownloadStageDone:
		// "The engine has nothing left to do." For usenet (no seeding stage
		// exists) this is the first point content is on disk. For a torrent
		// whose engine reaches Done without ever reporting Seeding, mapping
		// it here too is conservative rather than an arbitrary choice:
		// Completed and Seeding are both "ready to import" to importarr's
		// consumer (importarr/worker/fileimport/worker.go's own phase
		// switch), so which of the two this function picks changes no
		// downstream behaviour.
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseCompleted}
	default:
		// The CRD's own Enum marker on DownloadStage rejects any value
		// outside the eight named above before it ever reaches a live
		// object, so this default is unreachable against a real apiserver.
		// Treated the same as Stage=="" ("no telemetry yet") rather than a
		// panic.
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseAssigned}
	}
}

// isContentComplete reports whether phase means the content is complete on
// disk and importable: Completed and Seeding, by
// importarr/worker/fileimport.Worker.Handle's own phase switch (worker.go),
// plus Imported itself so a reconcile after the import has already
// happened continues to observe "complete" rather than flapping back to
// false once status.phase advances past the two phases the importer
// actually watches.
func isContentComplete(phase downloadv1alpha1.DownloadPhase) bool {
	switch phase {
	case downloadv1alpha1.DownloadPhaseCompleted, downloadv1alpha1.DownloadPhaseSeeding, downloadv1alpha1.DownloadPhaseImported:
		return true
	default:
		return false
	}
}
