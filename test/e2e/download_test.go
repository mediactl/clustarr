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
// grab's own new surface: the DownloadClient and Download controllers
// (D2-3, D2-4, D2-8a) and the torrent/usenet engines (D2-5, D2-6) against
// real fixture services, driving real transfers over cluster networking.
// Scenario 2 (RSS upgrade) is test/e2e/import_test.go's file, since its
// D2-relevant half is import's upgrade/recycle logic, not grab's.
//
// # Verified gaps this file was written against, not assumed
//
// This suite has never been executed (e2e execution is deferred by explicit
// user instruction until D1-D3 implementation is complete), so every wait
// below is written to fail with a NAMED reason rather than hang for its
// full timeout when a dependency is missing.
//
//  1. app/grab/run.go's setupControllers/setupEngine and
//     app/import/run.go's setupWorkers are task D2-8's ("wiring, RBAC,
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
//     D2-8 before wave 5's D2-10); if it is not, or regresses, grab's
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
// does not recognise, so app/import/worker/fileimport could never classify
// it as importable. X12c closed this too (test/fixtures/seeder.ContentName,
// test/fixtures/nntpstub.FileName): both now serve
// "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv", real bytes
// under a real, parseable movie-release name -- see
// helpers_test.go's waitForImportOutcome for the full reasoning. Every
// scenario below that reaches a completed transfer now calls
// waitForImportOutcome and asserts Imported for real.
package e2e

import (
	"testing"
)

// TestDownloadTorrentGrabToImportAttempt is scenario 1's grab+import
// slice: a Movie with real TMDB metadata, a real torrent DownloadClient/
// engine, a Download driven directly against test/fixtures/seeder (see
// newTorrentDownloadE2E's doc comment for why direct creation stands in for
// catalog's real grab decision here), through Completed/Seeding, then a
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
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13). The body
	// this scenario had is at the T1 base, git show 10f1a491:test/e2e/download_test.go; A8.4, F8.9 and
	// W6.15 rewrite it in T2.
	t.Skip("T2: scenario 1's download leg becomes a Search pick (A8.4, F8.9)")
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
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13). The body
	// this scenario had is at the T1 base, git show 10f1a491:test/e2e/download_test.go; A8.4, F8.9 and
	// W6.15 rewrite it in T2.
	t.Skip("T2: scenario 6 becomes a Search pick (A8.4, F8.9)")
}

// TestDownloadBlocklistThenRedownload is scenario 3: a failed download is
// blocklisted, and a fresh grab of a different release for the same target
// is unaffected.
//
// grab now writes download.clustarr.io/blocklisted itself, for a release
// fault (gap fix Y2: DownloadFailureReason.IsReleaseFault), but this test
// still sets the label by hand, which grab reads as failureReason
// "manual" -- an operator blocklisting a release. Driving a real release
// fault instead (a torrent with no seeder under a short
// spec.torrent.stallTimeout, or the nntp stub denying every article) is
// possible now and not done here. The hand-set label is safe against a
// REAL, running Download controller: derivePhase checks the label before it
// ever looks at engine-owned telemetry, so setting it produces
// DownloadPhaseBlocklisted regardless of what a real engine concurrently
// reports -- there is no field-manager race here, only a label (plain
// metadata, owned by this test's merge patch) the controller reads on its
// own next reconcile and never re-applies, since grab labels only a
// release fault it found itself.
//
// What else that reconcile now does, and this test does not assert: it
// records blocklistedUntil, DefaultBlocklistTTL (90 days) out; it publishes
// the failed and blocklisted events; the torrent engine removes the
// transfer and, per spec.removeDataOnDelete, its data; and catalog's
// catalog-redownload consumer (gap fix Y3) frees the Movie's grab lease
// (none here: a Download made by hand never took one) and publishes one
// redownload search. That search may grab a Download of its own if an
// Indexer is reachable from this namespace; nothing below counts
// Downloads, so it cannot fail the scenario. The second Download is still
// created directly, as before.
//
// BlocklistSweeper's deletion of the blocklisted Download
// (app/grab/controller/downloadclient/blocklist.go, registered in
// app/grab/run.go) is not exercised: blocklistedUntil is 90 days out, far
// past any scenario timeout. blocklist_envtest_test.go proves the sweep.
// This test proves grab's consumption of the label, not the sweep.
func TestDownloadBlocklistThenRedownload(t *testing.T) {
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13). The body
	// this scenario had is at the T1 base, git show 10f1a491:test/e2e/download_test.go; A8.4, F8.9 and
	// W6.15 rewrite it in T2.
	t.Skip("T2: scenario 3 reads the remove-blocklist intent and status.downloads[] (A8.4; TestABlockedReleaseShowsBlockedInASearch)")
}

// TestDownloadEnginePinSurvivesControllerRestart is D2-10's slice of
// scenario 4 (remaining-work.md: "kubectl rollout restart of every
// controller mid-flight, then a rerun of scenario 1's inputs: no duplicate
// Download... no duplicate files"). It proves grab's OWN idempotency
// under a real restart -- the SSA-complete-declaration discipline CLAUDE.md
// requires holding across a reconcile that starts from a cold cache -- the
// same property app/grab/controller/download/controller_envtest_test.go's
// TestEnginePinSurvivesAReconcileThatWouldOtherwisePickDifferently proves in
// envtest, now against a real restarted Deployment.
//
// The rest of scenario 4 -- rerunning scenario 1's inputs end to end and
// checking for a SECOND Download rather than a reused one -- exercises
// app/catalog/worker/grab/perform.go's SSA-idempotent create from a real grab
// decision, which is Phase C's own surface, not D2's, and is not re-proven
// here. TranscodeJob/SubtitleRequest duplication is Phase E/F's surface and
// likewise out of scope for this task.
func TestDownloadEnginePinSurvivesControllerRestart(t *testing.T) {
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13). The body
	// this scenario had is at the T1 base, git show 10f1a491:test/e2e/download_test.go; A8.4, F8.9 and
	// W6.15 rewrite it in T2.
	t.Skip("T2: scenario 4's engine pin is the entry's engine field (A8.4; TestEngineRestartKeepsSeeding)")
}
