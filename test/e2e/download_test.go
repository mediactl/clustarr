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

// Scenarios 1 (through import), 3, 4 and 6: a grab from a Search pick to a
// real transfer, a real import and a MediaFile. Since the Download kind went
// (A9.1) a grab is a status.downloads[] entry on its item, written by the
// manager alone, and a scenario starts one the way a person does: a Search
// whose spec.grab names the release (spec §6.13), or grabBest's automatic
// pick. Each scenario's Indexer answers interactive searches only, so no
// automatic search (the wanted sweep, a redownload) can grab beside the
// scenario's own pick.
//
// The releases are the torznab stub's Inception feed
// (test/fixtures/torznabstub/downloads.go): GUIDSeeder links to the
// seeder's .torrent under its real info hash, GUIDNZB (the usenet
// personality) to nntp-stub-a's .nzb, and GUIDGone to a 404. The seeder and
// the NNTP stubs serve the baked 1080p probe clip, named
// "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv"; the entry's
// owner attributes it, and the probe agrees with its 1080p.
//
// Written and vetted (go vet -tags e2e), not yet run on kind: Phase H runs
// them.
package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/test/fixtures/nntpstub"
	"github.com/mediactl/clustarr/test/fixtures/seed"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
	"github.com/mediactl/clustarr/test/fixtures/torznabstub"
)

// fixtureTorznabService is the torznab stub's Service, the download
// scenarios' indexer.
const fixtureTorznabService = "torznab-stub"

// TestDownloadTorrentGrabToImportAttempt is scenario 1's grab and import:
// a Movie with real TMDB metadata, a torrent DownloadClient and its engine,
// a person's pick of the seeder's release from a real Search, a real
// transfer from test/fixtures/seeder, and a real import to a MediaFile the
// Movie rolls up.
//
// The transcode and subtitle legs of scenario 1 are
// TestDownloadScenario1TranscodeLeg (transcode_test.go) and
// TestDownloadScenario1SubtitleLeg (subtitle_test.go), each from its own
// grab, so a failure in one leg cannot mask the others.
func TestDownloadTorrentGrabToImportAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)
	requireFixtureService(ctx, t, fixtureTorznabService)

	rf := newRootFolder(ctx, t, "e2e-dl1-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dl1-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Inception")
	dc := newTorrentDownloadClientE2E(ctx, t, "e2e-dl1-dc")
	idx := newDownloadIndexer(ctx, t, "e2e-dl1-idx", commonv1.ProtocolTorrent)

	s := searchAndGrab(ctx, t, movie, idx, false, torznabstub.GUIDSeeder)
	res := resultFor(t, &s, torznabstub.GUIDSeeder)
	require.True(t, res.Approved, "the seeder's release must be approved for Inception: %+v", res.Rejections)
	id := grabbedRef(t, &s, torznabstub.GUIDSeeder)

	e := waitForEntryImported(ctx, t, movie, id)
	hash := seederInfoHash(t)
	require.Equal(t, dc.Name, e.Client, "the grab is pinned to the scenario's DownloadClient")
	require.Equal(t, dc.Name+"-0", e.Engine, "and to its one engine instance")
	require.Equal(t, commonv1.ProtocolTorrent, e.Release.Protocol)
	require.Equal(t, hash, e.Release.InfoHash, "the release carries the info hash the seeder seeds")
	require.Equal(t, commonv1.GrabSourceInteractive, e.GrabbedBy, "a spec.grab pick is a person's")
	require.NotEmpty(t, e.OutputPath, "the engine reports where it put the payload")
	require.GreaterOrEqual(t, entryPhaseRank(e.Phase), entryPhaseRank(commonv1.DownloadPhaseCompleted))
	require.EqualValues(t, 1, e.Import.Files)

	mf := mediaFileForEntry(ctx, t, id)
	require.Equal(t, movie.Name, mf.Spec.MediaRef.Name)
	require.Truef(t, strings.HasPrefix(mf.Spec.Path, rf.Spec.Path+"/"), "imported to %s, outside the root folder %s", mf.Spec.Path, rf.Spec.Path)
	require.Equal(t, "Bluray-1080p", mf.Spec.Quality.Name, "the 1080p clip keeps its name's quality through the probe")
	require.NotNil(t, mf.Spec.ImportedFrom)
	require.Equal(t, commonv1.ProtocolTorrent, mf.Spec.ImportedFrom.Protocol)
	require.Equal(t, hash, mf.Spec.ImportedFrom.InfoHash)

	waitFor(t, ctx, 3*time.Minute, "Movie "+movie.Name+" has the imported file", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Movie
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.HasFile, nil
	}, describeMovie(client.ObjectKeyFromObject(movie)), describeEntries(client.ObjectKeyFromObject(movie)))
}

// TestDownloadUsenetNoInfoHashWithCrossServerFailover is scenario 6: a
// usenet DownloadClient with two providers (nntp-stub-a, priority 1;
// nntp-stub-b, priority 2), a pick of the usenet release -- which carries
// no info hash, the genuine usenet shape -- through a real transfer with a
// 430 cross-server failover, proven from the two stubs' own request logs,
// to an import.
//
// Par2 repair and RAR unpack, also named in scenario 6's text, are not
// exercised: the NNTP stub's post carries no par2 or archive files.
func TestDownloadUsenetNoInfoHashWithCrossServerFailover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNNTPStubAService)
	requireFixtureService(ctx, t, fixtureNNTPStubBService)
	requireFixtureService(ctx, t, fixtureTorznabService)

	rf := newRootFolder(ctx, t, "e2e-dl6-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dl6-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Inception")
	dc := newUsenetDownloadClientE2E(ctx, t, "e2e-dl6-dc")
	idx := newDownloadIndexer(ctx, t, "e2e-dl6-idx", commonv1.ProtocolUsenet)

	t0 := time.Now().Add(-time.Minute)
	s := searchAndGrab(ctx, t, movie, idx, false, torznabstub.GUIDNZB)
	id := grabbedRef(t, &s, torznabstub.GUIDNZB)

	e := waitForEntryImported(ctx, t, movie, id)
	require.Equal(t, dc.Name, e.Client)
	require.Equal(t, commonv1.ProtocolUsenet, e.Release.Protocol)
	require.Empty(t, e.Release.InfoHash, "a usenet release carries no info hash")

	// Cross-server 430 failover: config/e2e/nntp-stub.yaml starts
	// nntp-stub-a with --deny for the post's first article, and nntp-stub-b
	// must serve it after the primary refused. nntpstub.Build computes the
	// article id with the same formula BuildFromFile uses for the real clip
	// (it lives only in the fixture image), so a change to
	// fixtureNNTPSegmentBytes cannot silently desync the assertion.
	deniedID := nntpstub.Build(fixtureNNTPSegmentBytes, 0).Articles[0].ID
	aEntries, err := readNNTPRequests(fixtureNNTPADir, t0)
	require.NoError(t, err)
	bEntries, err := readNNTPRequests(fixtureNNTPBDir, t0)
	require.NoError(t, err)
	require.Truef(t, nntpEntriesContain(aEntries, deniedID, "denied"),
		"nntp-stub-a never logged denying %q: config/e2e must start it with --deny=%s for the failover proof to mean anything.\n%s",
		deniedID, deniedID, describeNNTPRequests(fixtureNNTPADir, t0)())
	require.Truef(t, nntpEntriesContain(bEntries, deniedID, "served"),
		"nntp-stub-b never logged serving %q after nntp-stub-a denied it.\n%s",
		deniedID, describeNNTPRequests(fixtureNNTPBDir, t0)())

	mf := mediaFileForEntry(ctx, t, id)
	require.Equal(t, movie.Name, mf.Spec.MediaRef.Name)
	require.NotNil(t, mf.Spec.ImportedFrom)
	require.Equal(t, commonv1.ProtocolUsenet, mf.Spec.ImportedFrom.Protocol)
	require.Empty(t, mf.Spec.ImportedFrom.InfoHash)
}

// TestDownloadBlocklistThenRedownload is scenario 3: a failed grab is
// blocklisted, a search shows the release blocked, and a fresh grab of a
// different release for the same item is unaffected -- nor is the shared
// engine.
//
// The failure is a real release fault, not a hand-set label: GUIDGone's
// link answers 404, so the engine cannot fetch its payload and the grab
// fails payloadUnavailable, which blocklists the release for the item
// (ADR-0019 §6.14) once the release index confirms the block. The redownload
// search that follows a release fault (R25) is automatic, and this
// scenario's Indexer answers interactive searches only, so it grabs nothing:
// the second grab is the scenario's own pick.
func TestDownloadBlocklistThenRedownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)
	requireFixtureService(ctx, t, fixtureTorznabService)

	rf := newRootFolder(ctx, t, "e2e-dlbl-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dlbl-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Inception")
	dc := newTorrentDownloadClientE2E(ctx, t, "e2e-dlbl-dc")
	idx := newDownloadIndexer(ctx, t, "e2e-dlbl-idx", commonv1.ProtocolTorrent)
	key := client.ObjectKeyFromObject(movie)

	first := searchAndGrab(ctx, t, movie, idx, false, torznabstub.GUIDGone)
	goneID := grabbedRef(t, &first, torznabstub.GUIDGone)
	blocked := waitForEntry(ctx, t, key, goneID, downloadCompleteTimeout, "Blocklisted, its block confirmed by the release index",
		func(e *catalogv1alpha1.DownloadEntry) (bool, error) {
			return e.Phase == commonv1.DownloadPhaseBlocklisted && e.Block != nil && e.Block.ConfirmedAt != nil, nil
		})
	require.Equal(t, commonv1.DownloadFailurePayloadUnavailable, blocked.FailureReason)
	require.Equal(t, catalogv1alpha1.BlockScopeItem, blocked.Block.Scope, "a release fault blocks the release for this item")

	// Blocklisting one grab must not knock the shared engine out of
	// readiness: a degraded-but-working engine could still complete the
	// next transfer while reporting unready, so it is asserted, not
	// inferred from the second grab succeeding.
	var liveDC downloadv1alpha1.DownloadClient
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(dc), &liveDC))
	require.True(t, isConditionTrue(liveDC.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady),
		"DownloadClient %s must stay EngineReady after a release fault:\n%s", dc.Name, describeDownloadClient(client.ObjectKeyFromObject(dc))())

	// The redownload: a person's pick of the seeder's release. The same
	// search shows the gone release blocked, which is where a person sees it.
	second := searchAndGrab(ctx, t, movie, idx, false, torznabstub.GUIDSeeder)
	gone := resultFor(t, &second, torznabstub.GUIDGone)
	require.False(t, gone.Approved, "a blocklisted release is not approved")
	require.Truef(t, hasRejection(gone.Rejections, decision.ReasonBlocklisted),
		"the gone release must read Blocklisted in the next search: %+v", gone.Rejections)
	seedID := grabbedRef(t, &second, torznabstub.GUIDSeeder)
	require.NotEqual(t, goneID, seedID)
	waitForEntryImported(ctx, t, movie, seedID)

	// The tombstone stays (BlockQuarantine), untouched by the second grab.
	var live catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, key, &live))
	tomb := entryOf(&live, goneID)
	require.NotNil(t, tomb, "the blocklisted entry is kept as a tombstone for an hour")
	require.Equal(t, commonv1.DownloadPhaseBlocklisted, tomb.Phase)
}

// TestDownloadEnginePinSurvivesControllerRestart is scenario 4's grab
// slice: the manager is restarted mid-flight, and the grab entry keeps its
// identity and its pinned client and engine, its phase does not regress,
// and the transfer still ends in exactly one import -- no second entry for
// the release, no second MediaFile. It proves the complete-declaration
// discipline (CLAUDE.md) across a reconcile that starts from a cold cache,
// against a real restarted Deployment.
func TestDownloadEnginePinSurvivesControllerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)
	requireFixtureService(ctx, t, fixtureTorznabService)

	rf := newRootFolder(ctx, t, "e2e-restart-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-restart-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Inception")
	newTorrentDownloadClientE2E(ctx, t, "e2e-restart-dc")
	idx := newDownloadIndexer(ctx, t, "e2e-restart-idx", commonv1.ProtocolTorrent)
	key := client.ObjectKeyFromObject(movie)

	s := searchAndGrab(ctx, t, movie, idx, false, torznabstub.GUIDSeeder)
	id := grabbedRef(t, &s, torznabstub.GUIDSeeder)
	before := waitForEntry(ctx, t, key, id, engineReadyTimeout, "assigned to an engine",
		func(e *catalogv1alpha1.DownloadEntry) (bool, error) {
			if entryPhaseRank(e.Phase) < 0 {
				return false, fmt.Errorf("entry %s reached %s (%s): %s", e.ID, e.Phase, e.FailureReason, e.Message)
			}
			return entryPhaseRank(e.Phase) >= entryPhaseRank(commonv1.DownloadPhaseAssigned) && e.Engine != "", nil
		})

	kubectlRolloutRestart(ctx, t, "manager")

	after := waitForEntryImported(ctx, t, movie, id)
	require.Equal(t, before.UID, after.UID, "the entry's uid must not change across a manager restart")
	require.Equal(t, before.Client, after.Client, "the client must not migrate across a manager restart")
	require.Equal(t, before.Engine, after.Engine, "the engine must not migrate across a manager restart")
	require.GreaterOrEqual(t, entryPhaseRank(after.Phase), entryPhaseRank(before.Phase),
		"the phase must not regress across a manager restart")

	var live catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, key, &live))
	n := 0
	for _, e := range live.Status.Downloads {
		if e.Release.GUID == torznabstub.GUIDSeeder {
			n++
		}
	}
	require.Equal(t, 1, n, "one entry for the release:\n%s", describeEntries(key)())
	require.Len(t, mediaFilesForEntry(ctx, t, id), 1, "one MediaFile for the grab")
}

// ---------------------------------------------------------------------------
// Grab helpers: an interactive-only Indexer, a Search pick, and the item's
// status.downloads[] entries.
// ---------------------------------------------------------------------------

// newDownloadIndexer creates an Indexer on the torznab stub's torrent
// personality (torznabstub.PathHealthy) or its usenet one
// (torznabstub.PathUsenet) and waits for it to be Ready. It answers
// interactive searches only -- a Search CR is user-invoked -- and polls no
// RSS, so nothing but the scenario's own pick can grab from it: the wanted
// sweep and a redownload search are automatic.
func newDownloadIndexer(ctx context.Context, t *testing.T, prefix string, protocol commonv1.Protocol) *indexv1alpha1.Indexer {
	t.Helper()
	apiPath := torznabstub.PathHealthy
	if protocol == commonv1.ProtocolUsenet {
		apiPath = torznabstub.PathUsenet
	}
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:                 "http://" + fixtureTorznabService + "." + Namespace + ".svc",
			Generic:                 &indexv1alpha1.GenericNewznab{Protocol: protocol, APIPath: apiPath},
			SecretRef:               &corev1.LocalObjectReference{Name: "torznab-fixture-credentials"},
			EnableRss:               new(false),
			EnableAutomaticSearch:   new(false),
			EnableInteractiveSearch: new(true),
			RssInterval:             metav1.Duration{Duration: 15 * time.Minute},
			RequestDelay:            &metav1.Duration{Duration: 100 * time.Millisecond},
			Priority:                25,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, idx))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), idx) })
	waitForIndexerReady(ctx, t, idx)
	return idx
}

// searchAndGrab creates a Search for movie on idx -- spec.grab naming guids
// (a person's pick: grabbedBy interactive, manual), or with grabBest the
// best approved result (an automatic grab: grabbedBy search, the importer's
// upgrade rules apply) -- and waits until it is Completed and every pick is
// an entry on the movie (status.grabbed[].downloadRef). A pick the owner
// refused fails at once with the refusal.
func searchAndGrab(ctx context.Context, t *testing.T, movie *catalogv1alpha1.Movie, idx *indexv1alpha1.Indexer, grabBest bool, guids ...string) catalogv1alpha1.Search {
	t.Helper()
	t0 := time.Now().Add(-time.Minute)
	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-pick"), Namespace: Namespace},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef:    &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
			IndexerRefs: []string{idx.Name},
			Limit:       50,
			Grab:        guids,
			GrabBest:    grabBest,
			TTL:         metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, srch))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), srch) })

	want := len(guids)
	if grabBest {
		want++
	}
	var done catalogv1alpha1.Search
	waitFor(t, ctx, searchCompletedTimeout+importAttemptTimeout, "Search "+srch.Name+" picks became grabs",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(srch), &done); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			if done.Status.Phase == catalogv1alpha1.SearchPhaseFailed {
				return false, fmt.Errorf("Search %s reached Failed", done.Name)
			}
			if done.Status.Phase != catalogv1alpha1.SearchPhaseCompleted {
				return false, nil
			}
			got := 0
			for _, g := range done.Status.Grabbed {
				if g.Error != "" {
					return false, fmt.Errorf("Search %s could not grab %s: %s", done.Name, g.GUID, g.Error)
				}
				if g.DownloadRef != "" {
					got++
				}
			}
			return got >= want, nil
		}, describePick(client.ObjectKeyFromObject(srch)), describeEntries(client.ObjectKeyFromObject(movie)),
		describeTorznabRequests(t0))
	return done
}

// grabbedRef is the entry id the Search's pick of guid became.
func grabbedRef(t *testing.T, s *catalogv1alpha1.Search, guid string) string {
	t.Helper()
	for _, g := range s.Status.Grabbed {
		if g.GUID == guid {
			require.NotEmpty(t, g.DownloadRef, "Search %s's pick of %s has no entry", s.Name, guid)
			return g.DownloadRef
		}
	}
	require.FailNowf(t, "no pick", "Search %s did not pick %s: grabbed=%+v", s.Name, guid, s.Status.Grabbed)
	return ""
}

// resultFor is the Search's verdict on guid.
func resultFor(t *testing.T, s *catalogv1alpha1.Search, guid string) commonv1.ReleaseDecision {
	t.Helper()
	for _, r := range s.Status.Results {
		if r.GUID == guid {
			return r
		}
	}
	require.FailNowf(t, "no result", "Search %s has no result %s (%d results)", s.Name, guid, len(s.Status.Results))
	return commonv1.ReleaseDecision{}
}

// hasRejection reports whether rs carries the decision reason r.
func hasRejection(rs []commonv1.Rejection, r decision.Reason) bool {
	for _, rej := range rs {
		if r.Of(rej) {
			return true
		}
	}
	return false
}

// entryOf is the movie's grab entry id, or nil.
func entryOf(m *catalogv1alpha1.Movie, id string) *catalogv1alpha1.DownloadEntry {
	for i := range m.Status.Downloads {
		if m.Status.Downloads[i].ID == id {
			return &m.Status.Downloads[i]
		}
	}
	return nil
}

// entryPhaseRank orders the phases a grab passes on its way to an import;
// a failure, a blocklist and a removal are -1.
func entryPhaseRank(p commonv1.DownloadPhase) int {
	switch p {
	case commonv1.DownloadPhasePending:
		return 0
	case commonv1.DownloadPhaseAssigned:
		return 1
	case commonv1.DownloadPhaseQueued:
		return 2
	case commonv1.DownloadPhaseDownloading, commonv1.DownloadPhasePaused:
		return 3
	case commonv1.DownloadPhaseCompleted:
		return 4
	case commonv1.DownloadPhaseSeeding, commonv1.DownloadPhaseImported:
		return 5
	}
	return -1
}

// waitForEntry polls the Movie at key until its entry id satisfies ok. An
// entry that does not exist yet keeps the poll going.
func waitForEntry(
	ctx context.Context, t *testing.T, key client.ObjectKey, id string, timeout time.Duration, desc string,
	ok func(*catalogv1alpha1.DownloadEntry) (bool, error),
) catalogv1alpha1.DownloadEntry {
	t.Helper()
	var got catalogv1alpha1.DownloadEntry
	waitFor(t, ctx, timeout, "grab "+id+" on Movie "+key.Name+": "+desc, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Movie
		if err := k8sClient.Get(ctx, key, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		e := entryOf(&live, id)
		if e == nil {
			return false, nil
		}
		got = *e
		return ok(e)
	}, describeEntries(key))
	return got
}

// waitForEntryImported waits until the grab's import is imported: as many
// MediaFiles as its plan places name the entry. An imported entry counts
// whatever its phase: a finished usenet transfer (removeCompleted) goes
// Removing ("removing: imported") right after its import, with no Seeding
// to wait in, and the smoke re-verification of 2026-10-08 saw scenario 6
// fail on exactly that. A failed, blocklisted or removed entry that never
// imported, or an import blocked, held or expired, fails at once with the
// entry's message.
func waitForEntryImported(ctx context.Context, t *testing.T, movie *catalogv1alpha1.Movie, id string) catalogv1alpha1.DownloadEntry {
	t.Helper()
	return waitForEntry(ctx, t, client.ObjectKeyFromObject(movie), id, downloadCompleteTimeout+importAttemptTimeout, "imported",
		func(e *catalogv1alpha1.DownloadEntry) (bool, error) {
			if e.Import != nil && e.Import.Phase == catalogv1alpha1.ImportPhaseImported {
				return true, nil
			}
			if entryPhaseRank(e.Phase) < 0 {
				return false, fmt.Errorf("entry %s reached %s (%s): %s", e.ID, e.Phase, e.FailureReason, e.Message)
			}
			if e.Import == nil {
				return false, nil
			}
			switch e.Import.Phase {
			case catalogv1alpha1.ImportPhaseBlocked, catalogv1alpha1.ImportPhaseHeld, catalogv1alpha1.ImportPhaseExpired:
				return false, fmt.Errorf("entry %s's import is %s (%s): %s", e.ID, e.Import.Phase, e.Import.Class, e.Import.Message)
			}
			return false, nil
		})
}

// mediaFilesForEntry lists the MediaFiles imported from grab id.
func mediaFilesForEntry(ctx context.Context, t *testing.T, id string) []catalogv1alpha1.MediaFile {
	t.Helper()
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(Namespace)))
	var out []catalogv1alpha1.MediaFile
	for _, mf := range list.Items {
		if mf.Spec.ImportedFrom != nil && mf.Spec.ImportedFrom.DownloadRef == id {
			out = append(out, mf)
		}
	}
	return out
}

// mediaFileForEntry waits for the one MediaFile imported from grab id and
// registers its cleanup.
func mediaFileForEntry(ctx context.Context, t *testing.T, id string) catalogv1alpha1.MediaFile {
	t.Helper()
	var got []catalogv1alpha1.MediaFile
	waitFor(t, ctx, importAttemptTimeout, "one MediaFile imported from grab "+id, func(ctx context.Context) (bool, error) {
		got = mediaFilesForEntry(ctx, t, id)
		return len(got) == 1, nil
	})
	mf := got[0]
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), mf.DeepCopy()) })
	return mf
}

// seederInfoHash is the info hash the seeder seeds: seeder.InfoHash of the
// clip hack/e2e.sh seeded onto /data, the same bytes the image bakes and the
// seeder copies.
func seederInfoHash(t *testing.T) string {
	t.Helper()
	h, err := seeder.InfoHash(hostPath("/data/" + fixtureDirName + "/" + seed.ClipName))
	require.NoError(t, err)
	return h.HexString()
}

// describeEntries renders a Movie's grab entries for a failure message.
func describeEntries(key client.ObjectKey) func() string {
	return func() string {
		var live catalogv1alpha1.Movie
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Sprintf("Movie %s is gone", key.Name)
			}
			return fmt.Sprintf("Movie %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("Movie %s phase=%q downloadPhase=%q entries=%d", key.Name, live.Status.Phase, live.Status.DownloadPhase, len(live.Status.Downloads))
		for _, e := range live.Status.Downloads {
			out += fmt.Sprintf("\n    entry %s guid=%s client=%s engine=%s phase=%s stage=%s failure=%s message=%q",
				e.ID, e.Release.GUID, e.Client, e.Engine, e.Phase, e.Stage, e.FailureReason, e.Message)
			if e.Import != nil {
				out += fmt.Sprintf("\n        import phase=%s class=%s files=%d attempts=%d message=%q",
					e.Import.Phase, e.Import.Class, e.Import.Files, e.Import.Attempts, e.Import.Message)
			}
			if e.Block != nil {
				out += fmt.Sprintf("\n        block scope=%s reason=%s confirmed=%t", e.Block.Scope, e.Block.Reason, e.Block.ConfirmedAt != nil)
			}
		}
		return out
	}
}

// describePick renders a Search's phase, its picks and its results'
// verdicts for a failure message.
func describePick(key client.ObjectKey) func() string {
	return func() string {
		out := describeSearch(key)()
		var live catalogv1alpha1.Search
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return out
		}
		for _, g := range live.Status.Grabbed {
			out += fmt.Sprintf("\n    grabbed guid=%s downloadRef=%q error=%q", g.GUID, g.DownloadRef, g.Error)
		}
		for _, r := range live.Status.Results {
			out += fmt.Sprintf("\n    result %s %q approved=%t rejections=%+v", r.GUID, r.Title, r.Approved, r.Rejections)
		}
		return out
	}
}
