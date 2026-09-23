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
// It used to hit the identical, permanent wall test/e2e/download_test.go's
// package doc comment used to document in full: test/fixtures/seeder used
// to publish its content as "clustarr-fixture.bin", an extension outside
// pkg/fsops.MediaExtensions, so fsops.Walk never reached processFile's
// upgrade check at all. X12c (docs/superpowers/plans/2026-09-23-gap-fixes.md)
// closed that: seeder.ContentName now serves real bytes named
// "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv" -- the SAME
// (resolution, source) as fixtureMovieFile below, with a REPACK tag that
// pkg/release/quality.go's detectRevision reads as a higher revision, so
// pkg/quality.Profile.UpgradeDecision's sameDef branch returns Upgrade
// against fixtureMovieFile's Version-1 original rather than
// FormatScoreNotHigher -- see waitForImportOutcome's own doc comment
// (helpers_test.go) for the full reasoning. This file now asserts the
// upgrade for real.
//
// Build-tagged e2e. Never executed against a kind cluster as of this
// writing (2026-09-23) -- see download_test.go's package doc comment for
// the non-permanent wiring gaps this depends on.
package e2e

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
	imp := waitForImportOutcome(ctx, t, dl, importAttemptTimeout)
	require.Len(t, imp.Imported, 1)
	require.NotEmpty(t, imp.Imported[0].MediaFileRef)

	// The superseded original must land in the recycle bin, not just
	// vanish: pkg/fsops.Recycle (root/<yyyy-mm-dd UTC>/<basename>, its own
	// doc comment) is what process.go calls before applying the new
	// MediaFile, ahead of deleting the old MediaFile object -- see this
	// function's own doc comment for why pc.existing (the planted
	// fixtureMovieFile) is non-nil here, which is what makes this the
	// upgrade path rather than TestDownloadTorrentGrabToImportAttempt's
	// first-import one.
	recycled := hostPath(path.Join(defaultRecycleBinLogical, time.Now().UTC().Format("2006-01-02"), fixtureMovieFile))
	waitFor(t, ctx, recycledOriginalTimeout, "recycled original at "+recycled, func(context.Context) (bool, error) {
		info, err := os.Stat(recycled)
		if err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return info.Size() == existing.Spec.SizeBytes, nil
	})
}
