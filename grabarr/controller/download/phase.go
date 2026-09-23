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

// phaseResult is what derivePhase computes.
type phaseResult struct {
	phase downloadv1alpha1.DownloadPhase

	// failureReason is why the Download failed. It is set for Failed and
	// Blocklisted and empty otherwise.
	failureReason downloadv1alpha1.DownloadFailureReason

	// blocklist is true when this reconcile must blocklist the release
	// itself: apply the blocklist label and record status.blocklistedUntil.
	// It is false for a Download already labelled, by grabarr or by hand.
	blocklist bool
}

// derivePhase computes dl's lifecycle phase from its own history
// (status.failureReason, status.blocklistedUntil, the blocklist label,
// spec.paused, status.import) and from what its engine reports
// (status.stage, status.engineFailureReason, status.isEncrypted,
// status.healthPaused). It is
// called only once status.engine is pinned (controller.go's "one-way door"),
// so Assigned is always a legal answer for a Download no telemetry has
// reached yet.
//
// # The order, and why each step is where it is
//
//  1. Imported is sticky and first: once importarr has reported success,
//     nothing the transfer does afterwards -- its own cleanup, a stale flag
//     -- revisits that verdict.
//  2. A failure. [failureOf] finds it, and the result is terminal:
//     Blocklisted when the label is on (whoever put it there); Blocklisted,
//     with blocklist set, for a release fault grabarr has not blocklisted
//     yet (design spec §8.3's "Failed -> Blocklisted", ruling in
//     DownloadFailureReason.IsReleaseFault); Failed otherwise -- a local
//     fault, or a release fault whose label an operator has since removed.
//  3. spec.paused, or the engine's health pause.
//  4. The engine's stage.
//
// # Why Failed is terminal
//
// status.failureReason, once recorded, is carried forward on every later
// reconcile (failureOf reads it first). The engine's report can go away --
// a torrent engine keeps a transfer's failure in memory, so a restart
// re-attaches it as though nothing happened -- but catalogarr has already
// read the Download as terminal and the redownload search (§8.3) may
// already have grabbed a replacement; letting the old Download come back to
// life would be a second grab of the same item. The engines remove a failed
// transfer once the phase says so, which is what makes the verdict stick on
// their side too.
//
// # Why un-blocklisting is removing the label
//
// grabarr applies the label only in the reconcile that also records
// blocklistedUntil, and asks for it again (blocklist=true) only while
// blocklistedUntil is unset. An operator who removes the label from a
// Download grabarr blocklisted therefore gets a Failed Download that stays
// un-blocklisted, rather than a label grabarr puts straight back. Deleting
// the Download works too; it is the blocklist entry.
//
// # What stays a strict subset of the rollup's switch
//
// Every value this function can produce -- Assigned, Queued, Downloading,
// Paused, Completed, Seeding, Imported, Failed, Blocklisted -- has a case in
// catalogarr/controller/rollup/downloadoverlay.go's DownloadOverlay switch
// (plan ruling R1). It never returns Pending (controller.go's pre-assignment
// applies) or Removing (the finalizer does not set it; doc.go).
//
// # progressPercent
//
// status.progressPercent does not appear below: Downloading covers the whole
// 1-99% range as one phase, so it discriminates between no two answers.
func derivePhase(dl *downloadv1alpha1.Download) phaseResult {
	if dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported {
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseImported}
	}

	labelled := isBlocklistLabelled(dl)
	reason := failureOf(dl, labelled)
	switch {
	case labelled:
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: reason}
	case reason.IsFailure() && reason.IsReleaseFault() && dl.Status.BlocklistedUntil == nil:
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: reason, blocklist: true}
	case reason.IsFailure():
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseFailed, failureReason: reason}
	}

	// Paused: "by spec.paused or by a health action" (DownloadPhasePaused's
	// own doc comment). The health action is the usenet engine's report,
	// status.healthPaused (DownloadClient spec.usenet.healthAction=pause):
	// the job waits for an operator, and a failure it later reaches still
	// wins above.
	if dl.Spec.Paused || dl.Status.HealthPaused {
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
		// exists) this is the first point content is on disk. A torrent
		// reaches it once it stops seeding -- its seed goal met -- or when
		// it never seeded. Completed and Seeding are both "ready to import"
		// to importarr's consumer (importarr/worker/fileimport/worker.go's
		// own phase switch), so which of the two this picks changes no
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

// failureOf is why dl failed, or "" if it has not. In order:
//
//   - status.failureReason, the verdict this controller already recorded. It
//     comes first so a failure is terminal (see derivePhase).
//   - status.engineFailureReason, the engine's report: missingArticles,
//     diskFull, writeError, timeout and encrypted from usenet; stalled,
//     diskFull, writeError and payloadMismatch from a torrent.
//   - status.isEncrypted, the engine's older signal for encrypted, kept so a
//     Download an engine flagged before engineFailureReason existed still
//     reads as encrypted.
//   - importRejected, read from importarr's status.import (never written
//     here): importarr refused every file.
//   - manual, for a Download an operator labelled blocklisted by hand while
//     nothing else had failed it -- the one user action there is for
//     failing a Download (the UI has none).
func failureOf(dl *downloadv1alpha1.Download, labelled bool) downloadv1alpha1.DownloadFailureReason {
	switch {
	case dl.Status.FailureReason.IsFailure():
		return dl.Status.FailureReason
	case dl.Status.EngineFailureReason.IsFailure():
		return dl.Status.EngineFailureReason
	case dl.Status.IsEncrypted:
		return downloadv1alpha1.DownloadFailureEncrypted
	case importRejected(dl):
		return downloadv1alpha1.DownloadFailureImportRejected
	case labelled:
		return downloadv1alpha1.DownloadFailureManual
	default:
		return ""
	}
}

// importRejected reports whether importarr's last word on dl is that it
// refused every file: status.import blocked, nothing imported, at least one
// rejection, and downloadv1alpha1.ImportMessageEveryFileRejected -- see that
// constant for why all four. importarr's file-import worker writes the same
// constant, so a rewording changes both sides at once.
func importRejected(dl *downloadv1alpha1.Download) bool {
	imp := dl.Status.Import
	return imp != nil &&
		imp.State == downloadv1alpha1.ImportPhaseBlocked &&
		len(imp.Imported) == 0 &&
		len(imp.Rejections) > 0 &&
		imp.Message == downloadv1alpha1.ImportMessageEveryFileRejected
}

// isBlocklistLabelled reports whether dl carries the blocklist label.
func isBlocklistLabelled(dl *downloadv1alpha1.Download) bool {
	return dl.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue
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
