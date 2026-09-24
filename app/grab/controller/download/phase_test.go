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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// TestDerivePhaseIsASubsetOfDownloadOverlay is R1's own guard, local to this
// package: every phase value derivePhase can produce must already have a
// case (explicit or default) in
// app/catalog/controller/rollup/downloadoverlay.go's DownloadOverlay switch.
// This package cannot import catalogarr (a dependency direction the module
// does not allow), so it cannot call DownloadOverlay directly; instead it
// pins the closed set derivePhase may return, which is the half of R1 this
// package owns. The other half -- that the set below matches
// DownloadOverlay's switch -- was verified by reading that source directly
// (doc comments in phase.go and controller.go cite the line numbers).
func TestDerivePhaseNeverProducesPendingOrRemoving(t *testing.T) {
	allowed := map[downloadv1alpha1.DownloadPhase]bool{
		downloadv1alpha1.DownloadPhaseAssigned:    true,
		downloadv1alpha1.DownloadPhaseQueued:      true,
		downloadv1alpha1.DownloadPhaseDownloading: true,
		downloadv1alpha1.DownloadPhasePaused:      true,
		downloadv1alpha1.DownloadPhaseCompleted:   true,
		downloadv1alpha1.DownloadPhaseSeeding:     true,
		downloadv1alpha1.DownloadPhaseImported:    true,
		downloadv1alpha1.DownloadPhaseFailed:      true,
		downloadv1alpha1.DownloadPhaseBlocklisted: true,
	}
	for _, tc := range derivePhaseCases(t) {
		res := derivePhase(tc.dl)
		assert.True(t, allowed[res.phase], "%s: derivePhase produced %q, which is not in the set this test pins", tc.name, res.phase)
	}
}

type derivePhaseCase struct {
	name string
	dl   *downloadv1alpha1.Download
	want phaseResult
}

func derivePhaseCases(t *testing.T) []derivePhaseCase {
	t.Helper()
	base := func() *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{
			Status: downloadv1alpha1.DownloadStatus{Engine: "qbit-0"},
		}
	}

	imported := base()
	imported.Status.Import = &downloadv1alpha1.ImportState{State: downloadv1alpha1.ImportPhaseImported}
	imported.Status.IsEncrypted = true // must be ignored: Imported is sticky and checked first

	blocklisted := base()
	blocklisted.Labels = map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue}
	blocklisted.Status.Stage = downloadv1alpha1.DownloadStageTransferring // must be ignored too

	encrypted := base()
	encrypted.Status.IsEncrypted = true

	paused := base()
	paused.Spec.Paused = true

	// The usenet engine's health action paused it; spec.paused is false.
	healthPaused := base()
	healthPaused.Status.Stage = downloadv1alpha1.DownloadStageTransferring
	healthPaused.Status.HealthPaused = true

	// A health-paused job that then failed -- its downloadTimeout passed
	// while it waited -- is failed, not paused.
	healthPausedThenFailed := base()
	healthPausedThenFailed.Status.HealthPaused = true
	healthPausedThenFailed.Status.EngineFailureReason = downloadv1alpha1.DownloadFailureTimeout

	noTelemetry := base()

	fetchingMetadata := base()
	fetchingMetadata.Status.Stage = downloadv1alpha1.DownloadStageFetchingMetadata

	transferring := base()
	transferring.Status.Stage = downloadv1alpha1.DownloadStageTransferring

	verifying := base()
	verifying.Status.Stage = downloadv1alpha1.DownloadStageVerifying

	repairing := base()
	repairing.Status.Stage = downloadv1alpha1.DownloadStageRepairing

	extracting := base()
	extracting.Status.Stage = downloadv1alpha1.DownloadStageExtracting

	publishing := base()
	publishing.Status.Stage = downloadv1alpha1.DownloadStagePublishing

	seeding := base()
	seeding.Status.Stage = downloadv1alpha1.DownloadStageSeeding

	done := base()
	done.Status.Stage = downloadv1alpha1.DownloadStageDone

	engineFailed := func(r downloadv1alpha1.DownloadFailureReason) *downloadv1alpha1.Download {
		dl := base()
		dl.Status.Stage = downloadv1alpha1.DownloadStageTransferring
		dl.Status.EngineFailureReason = r
		return dl
	}

	importedOverFailure := engineFailed(downloadv1alpha1.DownloadFailureStalled)
	importedOverFailure.Status.Import = &downloadv1alpha1.ImportState{State: downloadv1alpha1.ImportPhaseImported}

	noneIsNotAFailure := engineFailed(downloadv1alpha1.DownloadFailureNone)

	rejected := base()
	rejected.Status.Stage = downloadv1alpha1.DownloadStageDone
	rejected.Status.Import = &downloadv1alpha1.ImportState{
		State:      downloadv1alpha1.ImportPhaseBlocked,
		Message:    downloadv1alpha1.ImportMessageEveryFileRejected,
		Rejections: []string{"movie.mkv: quality SDTV is not in the profile"},
	}

	// A walk error on importarr's final attempt (a full library disk, say)
	// is blocked with rejections and nothing imported too; only the message
	// tells it apart, and it is not the release's fault.
	walkError := base()
	walkError.Status.Stage = downloadv1alpha1.DownloadStageDone
	walkError.Status.Import = &downloadv1alpha1.ImportState{
		State:      downloadv1alpha1.ImportPhaseBlocked,
		Message:    "fileimport: fsops: insufficient free space",
		Rejections: []string{"sample.mkv: sample"},
	}

	until := metav1.NewTime(time.Now().Add(time.Hour))

	// grabarr blocklisted it earlier; the engine's report has since gone (a
	// torrent engine restart forgets it), and the verdict must not.
	stillBlocklisted := base()
	stillBlocklisted.Labels = map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue}
	stillBlocklisted.Status.Stage = downloadv1alpha1.DownloadStageTransferring
	stillBlocklisted.Status.FailureReason = downloadv1alpha1.DownloadFailureStalled
	stillBlocklisted.Status.BlocklistedUntil = &until

	// An operator removed the label grabarr put on.
	lifted := base()
	lifted.Status.Stage = downloadv1alpha1.DownloadStageTransferring
	lifted.Status.FailureReason = downloadv1alpha1.DownloadFailureStalled
	lifted.Status.BlocklistedUntil = &until

	// A local fault recorded earlier stays Failed after the engine forgets it.
	stillFailed := base()
	stillFailed.Status.Stage = downloadv1alpha1.DownloadStageTransferring
	stillFailed.Status.FailureReason = downloadv1alpha1.DownloadFailureDiskFull

	// An operator blocklisted a Download that had failed for a local fault:
	// their call, and the recorded reason stands.
	labelledLocal := base()
	labelledLocal.Labels = map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue}
	labelledLocal.Status.FailureReason = downloadv1alpha1.DownloadFailureWriteError

	blocklistNow := func(r downloadv1alpha1.DownloadFailureReason) phaseResult {
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: r, blocklist: true}
	}
	failed := func(r downloadv1alpha1.DownloadFailureReason) phaseResult {
		return phaseResult{phase: downloadv1alpha1.DownloadPhaseFailed, failureReason: r}
	}

	return []derivePhaseCase{
		{"imported is sticky over encrypted", imported, phaseResult{phase: downloadv1alpha1.DownloadPhaseImported}},
		{"imported is sticky over an engine failure", importedOverFailure, phaseResult{phase: downloadv1alpha1.DownloadPhaseImported}},
		{"a hand-set blocklist label is a manual failure", blocklisted, phaseResult{
			phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: downloadv1alpha1.DownloadFailureManual,
		}},
		{"isEncrypted blocklists", encrypted, blocklistNow(downloadv1alpha1.DownloadFailureEncrypted)},
		{
			"missingArticles blocklists", engineFailed(downloadv1alpha1.DownloadFailureMissingArticles),
			blocklistNow(downloadv1alpha1.DownloadFailureMissingArticles),
		},
		{
			"stalled blocklists", engineFailed(downloadv1alpha1.DownloadFailureStalled),
			blocklistNow(downloadv1alpha1.DownloadFailureStalled),
		},
		{
			"timeout blocklists", engineFailed(downloadv1alpha1.DownloadFailureTimeout),
			blocklistNow(downloadv1alpha1.DownloadFailureTimeout),
		},
		{
			"encrypted from the engine blocklists", engineFailed(downloadv1alpha1.DownloadFailureEncrypted),
			blocklistNow(downloadv1alpha1.DownloadFailureEncrypted),
		},
		{
			"diskFull fails without blocklisting", engineFailed(downloadv1alpha1.DownloadFailureDiskFull),
			failed(downloadv1alpha1.DownloadFailureDiskFull),
		},
		{
			"writeError fails without blocklisting", engineFailed(downloadv1alpha1.DownloadFailureWriteError),
			failed(downloadv1alpha1.DownloadFailureWriteError),
		},
		{"an engine reason of none is not a failure", noneIsNotAFailure, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"every file rejected blocklists", rejected, blocklistNow(downloadv1alpha1.DownloadFailureImportRejected)},
		{"a blocked walk error is not importRejected", walkError, phaseResult{phase: downloadv1alpha1.DownloadPhaseCompleted}},
		{"a recorded blocklisting outlives the engine's report", stillBlocklisted, phaseResult{
			phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: downloadv1alpha1.DownloadFailureStalled,
		}},
		{"a lifted blocklist is not re-applied", lifted, failed(downloadv1alpha1.DownloadFailureStalled)},
		{"a recorded local fault is terminal", stillFailed, failed(downloadv1alpha1.DownloadFailureDiskFull)},
		{"a hand-set label keeps the recorded reason", labelledLocal, phaseResult{
			phase: downloadv1alpha1.DownloadPhaseBlocklisted, failureReason: downloadv1alpha1.DownloadFailureWriteError,
		}},
		{"spec.paused pauses", paused, phaseResult{phase: downloadv1alpha1.DownloadPhasePaused}},
		{"the engine's health pause pauses", healthPaused, phaseResult{phase: downloadv1alpha1.DownloadPhasePaused}},
		{"a failure outranks a health pause", healthPausedThenFailed, blocklistNow(downloadv1alpha1.DownloadFailureTimeout)},
		{
			"payloadMismatch blocklists", engineFailed(downloadv1alpha1.DownloadFailurePayloadMismatch),
			blocklistNow(downloadv1alpha1.DownloadFailurePayloadMismatch),
		},
		{"no telemetry yet stays assigned", noTelemetry, phaseResult{phase: downloadv1alpha1.DownloadPhaseAssigned}},
		{"fetchingMetadata queues", fetchingMetadata, phaseResult{phase: downloadv1alpha1.DownloadPhaseQueued}},
		{"transferring downloads", transferring, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"verifying downloads", verifying, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"repairing downloads", repairing, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"extracting downloads", extracting, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"publishing downloads", publishing, phaseResult{phase: downloadv1alpha1.DownloadPhaseDownloading}},
		{"seeding stage seeds", seeding, phaseResult{phase: downloadv1alpha1.DownloadPhaseSeeding}},
		{"done completes", done, phaseResult{phase: downloadv1alpha1.DownloadPhaseCompleted}},
	}
}

func TestDerivePhase(t *testing.T) {
	for _, tc := range derivePhaseCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePhase(tc.dl)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsContentComplete(t *testing.T) {
	complete := []downloadv1alpha1.DownloadPhase{
		downloadv1alpha1.DownloadPhaseCompleted,
		downloadv1alpha1.DownloadPhaseSeeding,
		downloadv1alpha1.DownloadPhaseImported,
	}
	for _, p := range complete {
		assert.True(t, isContentComplete(p), "%s must be content-complete", p)
	}

	notComplete := []downloadv1alpha1.DownloadPhase{
		downloadv1alpha1.DownloadPhasePending,
		downloadv1alpha1.DownloadPhaseAssigned,
		downloadv1alpha1.DownloadPhaseQueued,
		downloadv1alpha1.DownloadPhaseDownloading,
		downloadv1alpha1.DownloadPhasePaused,
		downloadv1alpha1.DownloadPhaseFailed,
		downloadv1alpha1.DownloadPhaseBlocklisted,
		downloadv1alpha1.DownloadPhaseRemoving,
	}
	for _, p := range notComplete {
		assert.False(t, isContentComplete(p), "%s must not be content-complete", p)
	}
}

// The failure-reason ruling (DownloadFailureReason.IsReleaseFault), pinned
// here because it is what decides whether derivePhase blocklists.
func TestOnlyReleaseFaultsBlocklist(t *testing.T) {
	release := []downloadv1alpha1.DownloadFailureReason{
		downloadv1alpha1.DownloadFailureMissingArticles, downloadv1alpha1.DownloadFailureEncrypted,
		downloadv1alpha1.DownloadFailureStalled, downloadv1alpha1.DownloadFailureTimeout,
		downloadv1alpha1.DownloadFailureImportRejected, downloadv1alpha1.DownloadFailureManual,
		downloadv1alpha1.DownloadFailurePayloadMismatch,
	}
	for _, r := range release {
		assert.Truef(t, r.IsReleaseFault(), "%s is the release's fault", r)
	}
	local := []downloadv1alpha1.DownloadFailureReason{
		downloadv1alpha1.DownloadFailureDiskFull, downloadv1alpha1.DownloadFailureWriteError,
		downloadv1alpha1.DownloadFailureNone, "",
	}
	for _, r := range local {
		assert.Falsef(t, r.IsReleaseFault(), "%q is not the release's fault", r)
	}
}
