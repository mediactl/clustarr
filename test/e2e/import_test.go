//go:build e2e

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

// Task D2-10 -- scenario 2 (RSS upgrade): "a better release appears in RSS
// -> upgrade grabbed and imported -> the old file is in .recycle"
// (docs/superpowers/plans/2026-09-18-remaining-work.md).
//
// The RSS-triggers-a-grab half of this scenario is Phase D1's surface, not
// D2's, and is already proven by test/e2e/indexer_test.go's scenario 17
// (TestIndexerFederatedSearchAndReleaseFirehose's firehoseTimeout wait
// asserts a matched Movie's status.pendingGrab from a real RSS poll through
// catalogarr's rss-matcher). Re-driving an RSS poll here would duplicate
// that proof, not extend it.
//
// What IS D2's own, new surface for this scenario is
// importarr/worker/fileimport's UPGRADE decision and its recycle of the
// superseded file: processConfig.processFile (process.go) compares a new
// candidate against pc.existing via profile.UpgradeDecision, and on a
// genuine upgrade calls fsops.Recycle on the old file's path before
// applying the new MediaFile. This file drives that path with a real,
// pre-existing MediaFile (planted and scanned the same way
// test/e2e/mediafile_test.go's TestMediaFileTwoWriter does) and a second,
// real completed torrent download for the same movie.
//
// It hits the identical, permanent wall test/e2e/download_test.go's package
// doc comment documents in full: test/fixtures/seeder always publishes its
// content as "clustarr-fixture.bin", an extension outside
// pkg/fsops.MediaExtensions, so fsops.Walk never reaches processFile's
// upgrade check at all for this download's content -- see
// helpers_test.go's importGapReason. waitForImportOutcomeOrSkip is called
// exactly as download_test.go's scenarios call it, and for the same reason.
//
// Build-tagged e2e. Never executed against a kind cluster as of this
// writing (2026-09-23) -- see download_test.go's package doc comment for
// the non-permanent wiring gaps this depends on.
package e2e

import (
	"context"
	"path"
	"testing"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// TestFileImportUpgradeAttempt is scenario 2's D2-relevant slice: see this
// file's package doc comment for what it proves and what it cannot.
func TestFileImportUpgradeAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)

	// The pre-existing file: the same RootFolder -> plant -> full-scan route
	// TestMediaFileTwoWriter and TestUIPipelineAndDownloadsPages both use, so
	// pc.existing is non-nil (process.go) by the time the upgrade download
	// completes -- without it, this would drive the FIRST-import path
	// (already covered by TestDownloadTorrentGrabToImportAttempt), not the
	// upgrade/recycle path this scenario is actually about.
	rf := newRootFolder(ctx, t, "e2e-imp2-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, fixtureMovieFolder, fixtureMovieFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	existing := files[0]
	movie := requireMovie(ctx, t, existing.Spec.MediaRef.Name)
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &movie) })
	waitForMovieSettled(ctx, t, &movie, "Inception")

	newTorrentDownloadClientE2E(ctx, t, "e2e-imp2-dc")
	torrentURL := "http://" + fixtureSeederService + "." + Namespace + ".svc/fixture.torrent"
	dl := newTorrentDownloadE2E(ctx, t, "e2e-imp2-dl", &movie, torrentURL, "guid-imp2-upgrade-1", movie.Spec.QualityProfileRef)

	waitForDownloadPhaseAtLeast(ctx, t, dl, downloadCompleteTimeout, downloadv1alpha1.DownloadPhaseCompleted)
	waitForImportOutcomeOrSkip(ctx, t, dl, importAttemptTimeout)
}
