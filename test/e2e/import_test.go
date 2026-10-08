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

// Scenario 2 (upgrade): "a better release appears -> upgrade grabbed and
// imported -> the old file is in .recycle"
// (docs/superpowers/plans/2026-09-18-remaining-work.md).
//
// The RSS-triggers-a-grab half is scenario 17's (indexer_test.go's firehose
// leg asserts a matched Movie's status.pendingGrab from a real RSS poll);
// re-driving an RSS poll here would duplicate it. This file drives the
// other half for real: an automatic grab (a grabBest Search, grabbedBy
// search) of a release that is an upgrade over the item's file, the
// importer's upgrade gate (importplan's gate, quality.Profile.UpgradeDecision)
// taking it, and the superseded file recycled.
//
// The existing file is planted and scanned as
// "Inception.2010.1080p.BluRay.x264-GROUP.mkv" (Bluray-1080p, v1); the
// seeder's release is torznabstub.TitleSeeder, a REPACK of the same quality
// -- a revision upgrade, which both the search's decision and the importer
// take although e2e-any's cutoff is met -- and its payload is the same 1080p
// clip under "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv".
// The gone release in the same feed is v1, so it is no upgrade and the
// automatic pick cannot be it.
package e2e

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/test/fixtures/torznabstub"
)

// TestFileImportUpgradeAttempt is scenario 2: see this file's package doc
// comment for what it proves.
func TestFileImportUpgradeAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)
	requireFixtureService(ctx, t, fixtureTorznabService)

	// The existing file, by the RootFolder -> plant -> full scan route
	// TestMediaFileTwoWriter uses, so the import has a file to compare
	// against: without it this would be the first-import path
	// TestDownloadTorrentGrabToImportAttempt already covers.
	rf := newRootFolder(ctx, t, "e2e-imp2-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, fixtureMovieFolder, fixtureMovieFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	existing := waitForMediaFileProbed(ctx, t, client.ObjectKeyFromObject(&files[0]))
	require.Equal(t, "Bluray-1080p", existing.Spec.Quality.Name)
	require.EqualValues(t, 1, max(existing.Spec.Revision.Version, 1), "the planted file is the original release")
	movie := requireMovie(ctx, t, existing.Spec.MediaRef.Name)
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &movie) })
	waitForMovieSettled(ctx, t, &movie, "Inception")

	newTorrentDownloadClientE2E(ctx, t, "e2e-imp2-dc")
	idx := newDownloadIndexer(ctx, t, "e2e-imp2-idx", commonv1.ProtocolTorrent)
	recycledBefore := countRecycled(t, fixtureMovieFile)

	// An automatic pick: grabBest takes the best approved result, the
	// REPACK; the gone v1 release is rejected as no upgrade over the file.
	s := searchAndGrab(ctx, t, &movie, idx, true)
	require.Len(t, s.Status.Grabbed, 1)
	require.Equal(t, torznabstub.GUIDSeeder, s.Status.Grabbed[0].GUID, "the best approved result is the REPACK")
	require.False(t, resultFor(t, &s, torznabstub.GUIDGone).Approved, "a v1 release is no upgrade over a v1 file")
	id := s.Status.Grabbed[0].DownloadRef

	e := waitForEntryImported(ctx, t, &movie, id)
	require.Equal(t, commonv1.GrabSourceSearch, e.GrabbedBy, "grabBest's pick is the automatic search's grab")
	require.False(t, e.Manual, "an automatic grab is not a person's, so the importer's upgrade gate applied")

	upgraded := mediaFileForEntry(ctx, t, id)
	require.Equal(t, movie.Name, upgraded.Spec.MediaRef.Name)
	require.Equal(t, "Bluray-1080p", upgraded.Spec.Quality.Name)
	require.EqualValues(t, 2, upgraded.Spec.Revision.Version, "the import froze the REPACK's revision")
	require.NotEqual(t, existing.Name, upgraded.Name)

	// The superseded original: its MediaFile goes, and its file goes into
	// the recycle bin (pkg/fsops.Recycle, <bin>/<yyyy-mm-dd>/<name>, "-2"
	// and on for a name the bin already holds) rather than vanishing.
	waitFor(t, ctx, recycledOriginalTimeout, "the original's MediaFile "+existing.Name+" deleted", func(ctx context.Context) (bool, error) {
		var mf catalogv1alpha1.MediaFile
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&existing), &mf)
		return apierrors.IsNotFound(err), nil
	}, describeMediaFile(client.ObjectKeyFromObject(&existing)))
	waitFor(t, ctx, recycledOriginalTimeout, "the original "+filePath+" recycled", func(context.Context) (bool, error) {
		_, err := os.Stat(hostPath(filePath))
		return os.IsNotExist(err) && countRecycled(t, fixtureMovieFile) > recycledBefore, nil
	})
}

// countRecycled counts the files in the default recycle bin
// (defaultRecycleBinLogical, every RootFolder this suite creates) recycled
// under name: fsops.Recycle keeps the base name and appends "-2", "-3" ...
// before the extension when the dated folder already holds it, so earlier
// runs' copies are counted too and only an increase means this run's.
func countRecycled(t *testing.T, name string) int {
	t.Helper()
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	n := 0
	err := filepath.WalkDir(hostPath(defaultRecycleBinLogical), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), stem) {
			n++
		}
		return nil
	})
	require.NoError(t, err)
	return n
}
