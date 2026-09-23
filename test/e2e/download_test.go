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

// Task D2-10 -- e2e scenarios 1 (through import), 3, 4 and 6
// (docs/superpowers/plans/2026-09-22-phase-d2-grabarr.md). This file covers
// grabarr's own new surface: the DownloadClient and Download controllers
// (D2-3, D2-4, D2-8a) and the torrent/usenet engines (D2-5, D2-6) against
// real fixture services, driving real transfers over cluster networking.
// Scenario 2 (RSS upgrade) is test/e2e/import_test.go's file, since its
// D2-relevant half is importarr's upgrade/recycle logic, not grabarr's.
//
// # Verified gaps this file was written against, not assumed
//
// This suite has never been executed (e2e execution is deferred by explicit
// user instruction until D1-D3 implementation is complete), so every wait
// below is written to fail with a NAMED reason rather than hang for its
// full timeout when a dependency is missing.
//
//  1. grabarr/run.go's setupControllers/setupEngine and
//     importarr/run.go's setupWorkers are task D2-8's ("wiring, RBAC,
//     readiness") job, owned by a different agent than this file, and that
//     file is explicitly not this task's to touch. Checked against source
//     TWICE while writing this file -- once at the start of this task, once
//     again just before finishing it: first as the empty stubs their own
//     doc comments described ("It registers nothing yet"),
//     then -- uncommitted, live in this same shared worktree -- with
//     downloadclient.NewReconciler, downloadclient.NewBlocklistSweeper,
//     download.NewReconciler, setupTorrentEngine/setupUsenetEngine and
//     fileimport.NewWorker/IndexMediaFileByTarget all registered. That
//     second read is NOT a guarantee this suite's first real execution will
//     see the same state: the change is uncommitted, so it could be
//     reverted, still being iterated on, or land in a different shape
//     before D2-11's gate gives it (and this file) a green build. Every
//     scenario below is written assuming the wiring is complete by
//     execution time (the plan's own wave ordering requires it -- wave 4's
//     D2-8 before wave 5's D2-10); if it is not, or regresses, grabarr's
//     DownloadClient will never report EngineReady and
//     waitForEngineReady's own timeout will name exactly that condition
//     rather than hang silently with no diagnosis.
//  2. config/e2e did not deploy test/fixtures/seeder or test/fixtures/
//     nntpstub as in-cluster Services -- confirmed by grepping config/ for
//     both names and finding nothing outside generated CRD schemas, and by
//     reading every task in Phase D2's plan file: none of them added such a
//     manifest. X12c (docs/superpowers/plans/2026-09-23-gap-fixes.md) closed
//     this: config/e2e/seeder.yaml and config/e2e/nntp-stub.yaml. Kept as
//     item 2 here, not deleted, because requireFixtureService
//     (test/e2e/helpers_test.go) still exists and still matters if either
//     manifest is ever dropped from config/e2e/kustomization.yaml's
//     resources list again.
//
// A third gap, independent of (1)-(2), used to block every "Imported"
// assertion PERMANENTLY: both fixtures always named their downloaded
// content "clustarr-fixture.bin", an extension pkg/fsops.MediaExtensions
// does not recognise, so importarr/worker/fileimport could never classify
// it as importable. X12c closed this too (test/fixtures/seeder.ContentName,
// test/fixtures/nntpstub.FileName): both now serve
// "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv", real bytes
// under a real, parseable movie-release name -- see
// helpers_test.go's waitForImportOutcome for the full reasoning. Every
// scenario below that reaches a completed transfer now calls
// waitForImportOutcome and asserts Imported for real.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/test/fixtures/nntpstub"
)

// TestDownloadTorrentGrabToImportAttempt is scenario 1's grabarr+importarr
// slice: a Movie with real TMDB metadata, a real torrent DownloadClient/
// engine, a Download driven directly against test/fixtures/seeder (see
// newTorrentDownloadE2E's doc comment for why direct creation stands in for
// catalogarr's real grab decision here), through Completed/Seeding, then a
// real import to Imported with a MediaFile.
//
// The transcode and subtitle legs of scenario 1 ("TranscodeJob Succeeded",
// "SubtitleRequest Satisfied") are Phase E/F's surface and are NOT in this
// function: they are test/e2e/transcode_test.go's
// TestDownloadScenario1TranscodeLeg and test/e2e/subtitle_test.go's
// TestDownloadScenario1SubtitleLeg, each starting its own independent
// download+import (not reusing this Movie/Download) so a failure in one
// leg cannot mask or block the other two.
func TestDownloadTorrentGrabToImportAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)

	rf := newRootFolder(ctx, t, "e2e-dl1-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dl1-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	settled := waitForMovieSettled(ctx, t, movie, "Inception")

	newTorrentDownloadClientE2E(ctx, t, "e2e-dl1-dc")
	torrentURL := "http://" + fixtureSeederService + "." + Namespace + ".svc/fixture.torrent"
	dl := newTorrentDownloadE2E(ctx, t, "e2e-dl1-dl", &settled, torrentURL, "guid-dl1-1", QualityProfileName)

	live := waitForDownloadPhaseAtLeast(ctx, t, dl, downloadCompleteTimeout, downloadv1alpha1.DownloadPhaseCompleted)
	require.NotEmpty(t, live.Status.ContentRoot, "a Completed/Seeding Download must publish status.contentRoot")
	require.NotZero(t, live.Status.DownloadedBytes, "a Completed/Seeding Download must report bytes fetched")

	imp := waitForImportOutcome(ctx, t, dl, importAttemptTimeout)
	require.Len(t, imp.Imported, 1)
	require.NotEmpty(t, imp.Imported[0].MediaFileRef)
}

// TestDownloadUsenetNoInfoHashWithCrossServerFailover is scenario 6: a
// usenet DownloadClient with two providers (nntp-stub-a, priority 1;
// nntp-stub-b, priority 2), a Download whose release carries NO infoHash --
// the genuine usenet shape -- through Completed, with a 430 cross-server
// failover proven from the two fixtures' own request logs.
//
// Par2 repair and RAR unpack, also named in scenario 6's text
// (remaining-work.md), are not exercised: test/fixtures/nntpstub's NZB
// deliberately carries no par2 or archive files (this task's own brief), and
// building one is out of D2-10's file scope (test/e2e/*.go only, not
// test/fixtures/*).
func TestDownloadUsenetNoInfoHashWithCrossServerFailover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNNTPStubAService)
	requireFixtureService(ctx, t, fixtureNNTPStubBService)

	rf := newRootFolder(ctx, t, "e2e-dl6-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dl6-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	settled := waitForMovieSettled(ctx, t, movie, "Inception")

	newUsenetDownloadClientE2E(ctx, t, "e2e-dl6-dc")

	t0 := time.Now().Add(-time.Minute)
	nzbURL := "http://" + fixtureNNTPStubAService + "." + Namespace + ".svc/fixture.nzb"
	dl := newUsenetDownloadE2E(ctx, t, "e2e-dl6-dl", &settled, nzbURL, "guid-dl6-1", QualityProfileName)
	require.Empty(t, dl.Spec.Release.InfoHash,
		"usenet releases carry no infoHash -- this is the fixed-CEL-rule regression shape pkg/crdcheck guards (c5e86d5)")

	live := waitForDownloadPhaseAtLeast(ctx, t, dl, downloadCompleteTimeout, downloadv1alpha1.DownloadPhaseCompleted)
	require.NotEmpty(t, live.Status.ContentRoot)

	// Cross-server 430 failover: nntp-stub-a is expected (per
	// config/e2e/nntp-stub.yaml and helpers_test.go's own section doc
	// comment) to deny the fixture's first article, and nntp-stub-b to
	// serve it after the primary refuses it.
	// nntpstub.Build(fixtureNNTPSegmentBytes, 0) recomputes the exact
	// article id deterministically -- Build and BuildFromFile share the
	// identical "seg<n>.<segmentBytes>@clustarr.fixture.test" formula
	// (BuildFromFile's own doc comment) -- rather than hard-coding it, so a
	// change to fixtureNNTPSegmentBytes (which MUST track config/e2e/
	// nntp-stub.yaml's --segment-bytes) cannot silently desync this
	// assertion from what the fixture would actually deny. Build, not
	// BuildFromFile, on purpose: the real baked clip nntp-stub-a's
	// --content-path names lives only inside the fixture image, and this
	// test process cannot reach it (main_test.go's own doc comment).
	deniedID := nntpstub.Build(fixtureNNTPSegmentBytes, 0).Articles[0].ID
	aEntries, aErr := readNNTPRequests(fixtureNNTPADir, t0)
	require.NoError(t, aErr)
	bEntries, bErr := readNNTPRequests(fixtureNNTPBDir, t0)
	require.NoError(t, bErr)

	require.Truef(t, nntpEntriesContain(aEntries, deniedID, "denied"),
		"nntp-stub-a never logged denying %q -- config/e2e must start it with --deny=%s "+
			"for this scenario's failover proof to mean anything.\n%s",
		deniedID, deniedID, describeNNTPRequests(fixtureNNTPADir, t0)())
	require.Truef(t, nntpEntriesContain(bEntries, deniedID, "served"),
		"nntp-stub-b never logged serving %q after nntp-stub-a denied it.\n%s",
		deniedID, describeNNTPRequests(fixtureNNTPBDir, t0)())

	imp := waitForImportOutcome(ctx, t, dl, importAttemptTimeout)
	require.Len(t, imp.Imported, 1)
	require.NotEmpty(t, imp.Imported[0].MediaFileRef)
}

// TestDownloadBlocklistThenRedownload is scenario 3: a failed download is
// blocklisted, and a fresh grab of a different release for the same target
// is unaffected.
//
// No component in this tree writes download.clustarr.io/blocklisted yet --
// grabarr/controller/download/phase.go's own doc comment: "Nothing in the
// tree sets this label yet (grep finds only readers...)" -- so this test
// plays that not-yet-built policy writer's part directly, the same
// "create it directly and say why" pattern test/e2e/ui_test.go uses for
// Download creation. This is safe against a REAL, running Download
// controller: derivePhase checks the blocklist label before it ever looks
// at engine-owned Stage telemetry, so setting the label produces
// DownloadPhaseBlocklisted regardless of what a real engine concurrently
// reports -- there is no field-manager race here, only a label (plain
// metadata) the controller reads on its own next reconcile.
//
// BlocklistSweeper's actual deletion of the blocklisted Download
// (grabarr/controller/downloadclient/blocklist.go) is not exercised: it
// needs status.blocklistedUntil, which nothing here sets (choosing a TTL is
// the same not-yet-built policy layer's job, and the CRD's own default,
// DefaultBlocklistTTL, is 90 days -- far past any scenario timeout), and it
// is itself only reachable once setupControllers registers it (task D2-8,
// see this file's package doc comment). This test proves grabarr's
// consumption of the label, not the sweep.
func TestDownloadBlocklistThenRedownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)

	rf := newRootFolder(ctx, t, "e2e-dlbl-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dlbl-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	settled := waitForMovieSettled(ctx, t, movie, "Inception")

	dc := newTorrentDownloadClientE2E(ctx, t, "e2e-dlbl-dc")
	torrentURL := "http://" + fixtureSeederService + "." + Namespace + ".svc/fixture.torrent"

	first := newTorrentDownloadE2E(ctx, t, "e2e-dlbl-dl1", &settled, torrentURL, "guid-dlbl-1", QualityProfileName)
	waitForDownloadPhaseAtLeast(ctx, t, first, engineReadyTimeout, downloadv1alpha1.DownloadPhaseAssigned)

	patchDownloadLabel(ctx, t, first, downloadv1alpha1.LabelBlocklisted, downloadv1alpha1.LabelBlocklistedValue)
	waitForDownloadPhaseExactly(ctx, t, first, engineReadyTimeout, downloadv1alpha1.DownloadPhaseBlocklisted)

	// Live, not degraded: blocklisting one Download is a label on that one
	// object (this function's own doc comment -- "there is no field-manager
	// race here, only a label"), and must not knock the SHARED
	// DownloadClient's engine StatefulSet out of readiness. Re-fetching and
	// asserting EngineReady is still True here, rather than trusting that
	// the redownload below would fail loudly if it were not, is what turns
	// "the engine happens to still work" into a proof: a degraded-but-not-
	// yet-failing engine could still complete one more transfer while
	// reporting unready, and the assertion below would never see that.
	var liveDC downloadv1alpha1.DownloadClient
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(dc), &liveDC))
	require.True(t, isConditionTrue(liveDC.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady),
		"DownloadClient %s must stay EngineReady after blocklisting an unrelated Download, not degrade:\n%s",
		dc.Name, describeDownloadClient(client.ObjectKeyFromObject(dc))())

	// Redownload: an independent Download for the same movie, a different
	// release guid, proceeds normally -- the earlier blocklisted Download
	// (a separate object) must not affect it.
	second := newTorrentDownloadE2E(ctx, t, "e2e-dlbl-dl2", &settled, torrentURL, "guid-dlbl-2", QualityProfileName)
	waitForDownloadPhaseAtLeast(ctx, t, second, downloadCompleteTimeout, downloadv1alpha1.DownloadPhaseCompleted)
}

// TestDownloadEnginePinSurvivesControllerRestart is D2-10's slice of
// scenario 4 (remaining-work.md: "kubectl rollout restart of every
// controller mid-flight, then a rerun of scenario 1's inputs: no duplicate
// Download... no duplicate files"). It proves grabarr's OWN idempotency
// under a real restart -- the SSA-complete-declaration discipline CLAUDE.md
// requires holding across a reconcile that starts from a cold cache -- the
// same property grabarr/controller/download/controller_envtest_test.go's
// TestEnginePinSurvivesAReconcileThatWouldOtherwisePickDifferently proves in
// envtest, now against a real restarted Deployment.
//
// The rest of scenario 4 -- rerunning scenario 1's inputs end to end and
// checking for a SECOND Download rather than a reused one -- exercises
// catalogarr/worker/grab/perform.go's SSA-idempotent create from a real grab
// decision, which is Phase C's own surface, not D2's, and is not re-proven
// here. TranscodeJob/SubtitleRequest duplication is Phase E/F's surface and
// likewise out of scope for this task.
func TestDownloadEnginePinSurvivesControllerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)

	rf := newRootFolder(ctx, t, "e2e-restart-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-restart-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	settled := waitForMovieSettled(ctx, t, movie, "Inception")

	newTorrentDownloadClientE2E(ctx, t, "e2e-restart-dc")
	torrentURL := "http://" + fixtureSeederService + "." + Namespace + ".svc/fixture.torrent"
	dl := newTorrentDownloadE2E(ctx, t, "e2e-restart-dl", &settled, torrentURL, "guid-restart-1", QualityProfileName)

	before := waitForDownloadPhaseAtLeast(ctx, t, dl, engineReadyTimeout, downloadv1alpha1.DownloadPhaseAssigned)
	require.NotEmpty(t, before.Spec.ClientRef)
	require.NotEmpty(t, before.Status.Engine)
	beforeClientRef, beforeEngine := before.Spec.ClientRef, before.Status.Engine

	kubectlRolloutRestart(ctx, t, "grabarr")

	after := waitForDownloadPhaseAtLeast(ctx, t, dl, engineReadyTimeout, downloadv1alpha1.DownloadPhaseAssigned)
	require.Equal(t, beforeClientRef, after.Spec.ClientRef, "clientRef must not migrate across a controller restart")
	require.Equal(t, beforeEngine, after.Status.Engine, "status.engine must not migrate across a controller restart")

	// The restart must not have regressed progress either: a genuinely
	// idempotent reconcile computes the SAME phase (or later, if the
	// transfer kept moving across the restart) from the same telemetry, not
	// an earlier one.
	require.GreaterOrEqual(t, downloadPhaseIndex(after.Status.Phase), downloadPhaseIndex(before.Status.Phase),
		"status.phase must not regress across a controller restart")
}
