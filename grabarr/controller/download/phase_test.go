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

	"github.com/stretchr/testify/assert"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// TestDerivePhaseIsASubsetOfDownloadOverlay is R1's own guard, local to this
// package: every phase value derivePhase can produce must already have a
// case (explicit or default) in
// catalogarr/controller/rollup/downloadoverlay.go's DownloadOverlay switch.
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

	return []derivePhaseCase{
		{"imported is sticky over encrypted", imported, phaseResult{phase: downloadv1alpha1.DownloadPhaseImported}},
		{"blocklist label overrides stage", blocklisted, phaseResult{phase: downloadv1alpha1.DownloadPhaseBlocklisted}},
		{"isEncrypted fails with reason", encrypted, phaseResult{
			phase: downloadv1alpha1.DownloadPhaseFailed, failureReason: downloadv1alpha1.DownloadFailureEncrypted,
		}},
		{"spec.paused pauses", paused, phaseResult{phase: downloadv1alpha1.DownloadPhasePaused}},
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
