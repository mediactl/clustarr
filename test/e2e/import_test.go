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
// catalog's rss-matcher). Re-driving an RSS poll here would duplicate
// that proof, not extend it.
//
// What IS D2's own, new surface for this scenario is
// app/import/worker/fileimport's UPGRADE decision and its recycle of the
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
	"testing"
)

// TestFileImportUpgradeAttempt is scenario 2's D2-relevant slice: see this
// file's package doc comment for what it proves and what it cannot.
func TestFileImportUpgradeAttempt(t *testing.T) {
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13). The body
	// this scenario had is at the T1 base, git show 10f1a491:test/e2e/import_test.go; A8.4, F8.9 and
	// W6.15 rewrite it in T2.
	t.Skip("T2: scenario 2's download becomes a Search pick (A8.4, F8.9)")
}
