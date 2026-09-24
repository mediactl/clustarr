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

// Scenario 17 -- Indexer, federated search and the release firehose (M2).
//
// Four things, against the in-cluster Torznab fixture
// (test/fixtures/torznabstub, deployed by config/e2e/torznab-stub.yaml):
//
//  1. an Indexer pointed at the fixture reconciles to healthy, with
//     status.caps from a LIVE capabilities fetch and status.protocol resolved;
//  2. a Search CR returns RANKED results -- snapshot -> rpc.indexarr.search ->
//     pkg/decision -> RankAndCap -> status.results, the whole Phase C path
//     that until Phase D1 called into silence;
//  3. the release firehose reaches catalogarr's rss-matcher with a legal
//     "<namespace>/<indexer>" envelope key, asserted by what the MATCHER DID;
//  4. health and backoff are observable: a failing indexer escalates, is
//     disabled, and is then neither queried by the fan-out nor polled again.
//
// Four Test functions, not one: scenarioTimeout is 15 minutes and each of
// these has its own multi-minute wait, so a failure in one must not bury the
// others' planted state in the same cleanup stack.
package e2e

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/torznab"
	"github.com/mediactl/clustarr/test/fixtures/torznabstub"
)

const (
	// fixtureSearchTmdbID is the fixture-owned TMDB movie the fixture
	// indexer's t=movie feed carries three releases for. It is NOT 27205:
	// TestLibraryRescan already owns a Movie for Inception against the
	// permissive single-tier e2e-any profile, and a scenario sharing it would
	// be mutating another scenario's object.
	fixtureSearchTmdbID = 900100

	// fixtureFirehoseTmdbID is the second fixture-owned movie, carried by the
	// fixture's one-item RSS feed, so the firehose release can match only the
	// Movie the firehose scenario creates.
	fixtureFirehoseTmdbID = 900101

	// The fixture's release titles. They exist nowhere else in the repo, on
	// disk, or in any CR, which is what makes asserting them prove the data
	// came through the wire rather than from somewhere convenient.
	release1080p  = "Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR"
	release720p   = "Fixture.Search.Film.2019.720p.WEBRip.x264-CLUSTARR"
	release480p   = "Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR"
	firehoseTitle = "Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR"
)

// TestIndexerHealthAndCaps is scenario 17's first leg: an Indexer pointed at
// the in-cluster fixture reconciles to healthy off a REAL capabilities fetch.
func TestIndexerHealthAndCaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	t0 := time.Now()

	// apiPath "" takes the CRD default "/api", which is the fixture's healthy
	// personality; enableRss false keeps this leg from publishing onto the
	// firehose and racing TestIndexerReleaseFirehose.
	idx := newIndexer(ctx, t, "e2e-idx-caps", "", 15*time.Minute, false)
	live := waitForIndexerReady(ctx, t, idx)

	// Authenticated, separately from Ready: the fixture answers Newznab error
	// 100 without the apikey, so this condition being True is the only proof
	// that spec.secretRef was read and forwarded. If the Secret plumbing
	// regressed, caps would be nil and the wait above would have timed out
	// with error-100 lines in the request log.
	require.True(t, isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionAuthenticated))

	// Resolved from spec.generic, not guessed. A regression here reads as an
	// empty column in `kubectl get indexers` and an indexer that the
	// protocol-gated parts of the decision engine silently never match.
	require.Equal(t, commonv1.ProtocolTorrent, live.Status.Protocol)
	require.NotEmpty(t, live.Status.Privacy,
		"status.privacy must be resolved for a generic indexer, not left blank")

	// The caps document, field by field, against
	// test/fixtures/torznabstub/testdata/caps.xml. These numbers exist in
	// exactly one place in the repo, so they cannot have come from a default.
	caps := live.Status.Caps
	require.EqualValues(t, 100, caps.LimitsMax)
	require.EqualValues(t, 50, caps.LimitsDefault)
	require.True(t, caps.SupportsRawSearch,
		`testdata/caps.xml sets searchEngine="raw" on <search>`)

	// RULING R5, and the single most regression-prone assertion here.
	// Caps.Modes is keyed by torznab.SearchMode's WIRE values, which are what
	// idxstatus.SupportsMode and torznab.Caps.Supports compare against. The
	// CRD's own doc comment used to say "search, tv-search, movie-search", and
	// there is no enum marker, so a caps fetch keyed the wrong way would
	// populate status, look entirely plausible in kubectl, and cause
	// SupportsMode to NEVER match -- every indexer silently skipped, every
	// search empty, nothing logged.
	require.Contains(t, caps.Modes, string(torznab.ModeMovieSearch)) // "movie"
	require.Contains(t, caps.Modes, string(torznab.ModeTVSearch))    // "tvsearch"
	require.NotContains(t, caps.Modes, "movie-search",
		"Caps.Modes is keyed by the wire value, not by the caps ELEMENT name (R5)")
	require.NotContains(t, caps.Modes, "tv-search")
	require.Contains(t, caps.Modes[string(torznab.ModeMovieSearch)], "tmdbid")
	require.Contains(t, caps.Modes[string(torznab.ModeTVSearch)], "tvdbid")

	// caps.xml advertises music-search available="no", and the projection
	// DROPS unavailable modes rather than recording them as unavailable
	// (indexarr/controller/indexer/caps.go: status.SupportsMode treats key
	// presence as availability, so an unavailable key would advertise a search
	// the indexer answers with error 203). The D1-9 brief asserted the
	// opposite; the tree wins, and this pins the tree's behaviour so the two
	// readings cannot drift apart again unnoticed.
	require.NotContains(t, caps.Modes, string(torznab.ModeMusicSearch),
		"an unavailable mode must be absent from status.caps.modes, because key presence IS availability")

	// The category tree survived the flattening into the CRD's two-level
	// Category/SubCategory shape.
	byID := map[int32]indexv1alpha1.Category{}
	for _, c := range caps.Categories {
		byID[c.ID] = c
	}
	require.Contains(t, byID, int32(2000))
	require.Contains(t, byID, int32(5000))
	var subIDs []int32
	for _, s := range byID[2000].Sub {
		subIDs = append(subIDs, s.ID)
	}
	require.ElementsMatch(t, []int32{2030, 2040, 2050}, subIDs)

	// Live, not cached or stale: a caps request logged by the fixture after
	// this scenario began. The Indexer object is created fresh with a unique
	// name each run, so its status cannot be left over -- but asserting the
	// request closes the door on a future caps cache too, and it is the one
	// assertion that would still hold if someone added one.
	reqs := torznabRequestsMatching(t, t0, torznabstub.PathHealthy, "caps")
	var sawCaps bool
	for _, r := range reqs {
		if r.Status == http.StatusOK {
			sawCaps = true
		}
	}
	require.True(t, sawCaps,
		"no successful t=caps request reached the fixture during this scenario; %s",
		describeTorznabRequests(t0)())
}

// TestIndexerSearchReturnsRankedResults is scenario 17's centre of gravity:
// the first time catalogarr's search path has had a server on the other end of
// rpc.indexarr.search. Everything from the Search CR through the snapshot, the
// RPC, pkg/decision and RankAndCap to status.results runs for real, against
// real XML off a real HTTP server.
func TestIndexerSearchReturnsRankedResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	t0 := time.Now()

	// A two-tier profile, NOT config/e2e's e2e-any. e2e-any has a single tier,
	// so every quality in it compares EQUAL and the ordering of the two
	// approved releases would fall through to size and seeders -- an assertion
	// that passes for the wrong reason. Two tiers make the ranking a statement
	// about quality, which is what is under test.
	profile := newRankedQualityProfile(ctx, t, "e2e-idx-qp", []tier{
		{name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
		{name: "Web720", qualities: []string{"WEBRip-720p", "WEBDL-720p"}},
	})

	rf := newRootFolder(ctx, t, "e2e-idx-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	idx := newIndexer(ctx, t, "e2e-idx-search", "", 15*time.Minute, false)
	waitForIndexerReady(ctx, t, idx)

	movie := newMovie(ctx, t, "e2e-idx-movie", fixtureSearchTmdbID, profile.Name, rf.Name,
		catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Fixture Search Film")

	// spec.indexerRefs pins the fan-out to this scenario's indexer, so
	// status.indexerOutcomes is deterministic however many Indexers other
	// scenarios have left in the namespace.
	done := runSearch(ctx, t, movie, []string{idx.Name})

	// One outcome, for THIS indexer, keyed by the Indexer's OBJECT name.
	//
	// Correction C1: the subject token, the envelope key's second segment and
	// ReleaseInfo.IndexerRef must all be the object name, never a display
	// name. If indexarr used a display name here, this outcome's Name would
	// not match any Indexer and rssmatcher's indexer-priority lookup would
	// silently fall back to the default -- a release that arrives, matches and
	// ranks against a priority resolving to nothing.
	require.Len(t, done.Status.IndexerOutcomes, 1)
	out := done.Status.IndexerOutcomes[0]
	require.Equal(t, idx.Name, out.Name,
		"the outcome must be keyed by the Indexer's object name, not its display name (C1)")
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, out.State, "error=%q", out.Error)
	require.EqualValues(t, 3, out.Count, "testdata/movie_900100.xml holds three items")

	// Three results, ranked 1..3, approved before rejected.
	require.Len(t, done.Status.Results, 3)
	for i, r := range done.Status.Results {
		require.EqualValues(t, i+1, r.Rank, "Rank is the 1-based position in the final list")
		require.Equal(t, idx.Name, r.IndexerRef, "every result carries the object name (C1)")
		require.Equal(t, commonv1.ProtocolTorrent, r.Protocol)
	}

	// The ordering assertion, and the reason testdata/movie_900100.xml is
	// deliberately WORST-FIRST. The feed's arrival order is 480p, 720p, 1080p;
	// the ranked order must be the reverse. A decision engine that regressed
	// to a pass-through -- or a fan-out that forwarded the indexer's own
	// ordering -- would put the 480p release at rank 1 and fail here, rather
	// than quietly agreeing with the wire.
	require.Equal(t, release1080p, done.Status.Results[0].Title)
	require.True(t, done.Status.Results[0].Approved,
		"rejections=%+v", done.Status.Results[0].Rejections)
	require.Equal(t, "Bluray-1080p", done.Status.Results[0].Quality.Name)

	require.Equal(t, release720p, done.Status.Results[1].Title)
	require.True(t, done.Status.Results[1].Approved,
		"rejections=%+v", done.Status.Results[1].Rejections)

	// The 480p release is below the profile's lowest tier: present, with a
	// reason, and last. "Present with a reason" is the never-guess invariant
	// in its search form -- a release the engine turned down is reported, not
	// dropped.
	last := done.Status.Results[2]
	require.Equal(t, release480p, last.Title)
	require.False(t, last.Approved)
	require.NotEmpty(t, last.Rejections, "a rejected release must say why")

	// Wire fields survived the whole projection: torznab.Release ->
	// rss.ProjectRelease -> schema.Release -> ReleaseInfo -> the CRD.
	top := done.Status.Results[0]
	require.EqualValues(t, 8589934592, top.SizeBytes)
	require.NotNil(t, top.Seeders)
	require.EqualValues(t, 120, *top.Seeders)
	require.Equal(t, "3333333333333333333333333333333333333333", top.InfoHash)
	require.Equal(t, "900100", top.IDs[commonv1.IDKeyTMDB])
	require.NotNil(t, top.PublishedAt, "pubDate must survive as PublishedAt")

	// Live, and id-based. Two separate claims, both from the request log:
	//   - a t=movie request reached the fixture during THIS scenario, so the
	//     results cannot have come from indexarr's local release index
	//     (rpc.indexarr.query's store) or any future cache;
	//   - it carried the provider id, not free text, which is what ruling R11
	//     says the ids-only caller produces. A regression to q= would show up
	//     here as a query with no tmdbid/imdbid, and the fixture would have
	//     answered the empty feed.
	movieReqs := torznabRequestsMatching(t, t0, torznabstub.PathHealthy, "movie")
	require.NotEmpty(t, movieReqs, "indexarr never issued a t=movie query to the fixture")
	var idBased bool
	for _, r := range movieReqs {
		v, err := url.ParseQuery(r.Query)
		require.NoError(t, err)
		// torznab.Query.Values strips the "tt" prefix from an IMDb id before
		// putting it on the wire, so the fixture sees 9000100, not tt9000100.
		if v.Get("tmdbid") == "900100" || v.Get("imdbid") == "9000100" {
			idBased = true
		}
	}
	require.True(t, idBased,
		"the fan-out must query by provider id, not free text; requests=%+v", movieReqs)
}

// TestIndexerReleaseFirehose is scenario 17's third leg: a release published
// by indexarr's RSS worker reaches catalogarr's rss-matcher with a legal
// envelope key, and the matcher acts on it.
//
// It asserts what the MATCHER DID, not what indexarr published. The matcher
// events.Discard()s -- straight to the DLQ, bypassing MaxDeliver -- on any
// envelope key it cannot strings.Cut on "/", so a wrong key is silent and
// invisible: indexarr's own status would still show lastRssNewCount > 0, the
// stream would still show the message, and nothing anywhere would report a
// failure. Only a downstream effect distinguishes "published" from "arrived".
//
// The effect chosen is Movie.status.pendingGrab, not a Download. A DelayProfile
// with a 12-hour torrent delay stops grab.Decide before performGrab, so this
// leg proves the whole path -- decode, key-split into the right namespace,
// match, resolve, evaluate, delay, apply -- without creating a Download, which
// is D2's subject and D2's scenario.
func TestIndexerReleaseFirehose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	t0 := time.Now()

	// Tiers chosen so the RSS release (WEBDL-1080p) is approved but NOT
	// top-tier. grab.Bypasses skips the delay when BypassIfHighestQuality is
	// set and the release is at index 0, and that flag DEFAULTS TO TRUE. The
	// DelayProfile below turns it off explicitly; this profile makes the
	// assertion hold even if it were left on. Two independent reasons, because
	// a delayed grab is the entire observable.
	profile := newRankedQualityProfile(ctx, t, "e2e-fh-qp", []tier{
		{name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
		{name: "Web1080", qualities: []string{"WEBDL-1080p", "WEBRip-1080p"}},
	})
	delay := newDelayProfile(ctx, t, "e2e-fh-dp", 720 /* minutes */)
	rf := newRootFolder(ctx, t, "e2e-fh-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

	// ORDER MATTERS, and this is the trap. CLUSTARR_RELEASES dedups on
	// Msg-Id = MsgIDForRelease(indexerName, guid) for TWO HOURS. The fixture's
	// RSS feed holds exactly one item, so the release is published once per
	// indexer and never again inside that window. If the Movie is not already
	// monitored, available and resolvable when that single publish lands, the
	// matcher correctly declines it -- and there is no second chance. So:
	// create the Movie, wait for it to be fully settled, and only THEN create
	// the RSS-enabled Indexer whose first poll publishes the release.
	movie := newMovie(ctx, t, "e2e-fh-movie", fixtureFirehoseTmdbID, profile.Name, rf.Name,
		catalogv1alpha1.MinimumAvailabilityAnnounced)
	patchMovieDelayProfile(ctx, t, movie, delay.Name)
	// minimumAvailability: announced is the second guarantee behind the
	// fixture TMDB entry: catalogarr/controller/movie.Availability returns
	// "always available" for announced without consulting metadata at all, so
	// even a metadata refresh that failed could not leave this Movie
	// unavailable and the release temporarily rejected.
	waitForMovieSettled(ctx, t, movie, "Fixture Firehose Film")

	// A unique object name per run is what keeps that 2h dedup window from
	// suppressing this run's publish -- the indexer name is half the Msg-Id.
	// uniqueName already guarantees it; this comment records that it is
	// load-bearing here and not merely hygienic.
	idx := newIndexer(ctx, t, "e2e-fh-idx", "", time.Minute, true /* enableRss */)
	waitForIndexerReady(ctx, t, idx)

	// indexarr's own side first, so a failure is attributed correctly: if the
	// poll itself never happened, that is the RSS worker's bug, not the
	// matcher's.
	waitFor(t, ctx, firehoseTimeout, "Indexer "+idx.Name+" published an RSS poll",
		func(ctx context.Context) (bool, error) {
			var live indexv1alpha1.Indexer
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(idx), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Status.LastRssAt != nil && live.Status.IndexedReleases >= 1, nil
		}, describeIndexer(client.ObjectKeyFromObject(idx)), describeTorznabRequests(t0))

	// The arrival assertion. status.pendingGrab is written by catalogarr's
	// grab path under ManagerCatalogarrGrab, and it can only exist if the
	// release was decoded, its envelope key split into THIS namespace, matched
	// to this Movie by tmdbid, resolved, evaluated and delayed. The title is
	// the fixture's string, which appears nowhere on disk, in any CR, or in
	// any other fixture.
	//
	// If the envelope key regressed to something without a "/", this wait
	// times out while every other signal stays green -- which is precisely the
	// failure this assertion exists to make visible.
	var delayed catalogv1alpha1.Movie
	waitFor(t, ctx, firehoseTimeout, "Movie "+movie.Name+" status.pendingGrab from the firehose",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &delayed); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return delayed.Status.PendingGrab != nil, nil
		}, describeMovie(client.ObjectKeyFromObject(movie)),
		describeIndexer(client.ObjectKeyFromObject(idx)),
		describeTorznabRequests(t0))

	pg := delayed.Status.PendingGrab
	require.Equal(t, firehoseTitle, pg.ReleaseTitle,
		"the pending release's title can only have come from the fixture's rss.xml")
	require.Equal(t, commonv1.ProtocolTorrent, pg.Protocol)
	// ~12h out, from the scenario's own DelayProfile. A grabAt only minutes
	// away would mean the profile was not resolved and the zero-value spec was
	// used -- which is also the state in which performGrab would have run and
	// created a Download.
	require.True(t, pg.GrabAt.After(time.Now().Add(11*time.Hour)),
		"pendingGrab.grabAt %s does not reflect the scenario's 720-minute torrent delay", pg.GrabAt)

	// The Movie reconciler recomputed Phase from pendingGrab under its own
	// field manager -- the two-writer split round-tripping through the
	// apiserver, not just one manager's apply landing.
	waitFor(t, ctx, 3*time.Minute, "Movie "+movie.Name+" phase Delayed",
		func(ctx context.Context) (bool, error) {
			var live catalogv1alpha1.Movie
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Status.Phase == catalogv1alpha1.MoviePhaseDelayed, nil
		}, describeMovie(client.ObjectKeyFromObject(movie)))

	// Nothing was grabbed: this leg belongs to D1, and a Download here would
	// mean the delay was bypassed and D2's scenarios are now racing an object
	// they did not create.
	var dls downloadv1alpha1.DownloadList
	require.NoError(t, k8sClient.List(ctx, &dls, client.InNamespace(Namespace)))
	for _, d := range dls.Items {
		require.NotEqual(t, firehoseTitle, d.Spec.Release.Title,
			"the delayed release must not have been grabbed")
	}
}

// TestIndexerFailureBackoff is scenario 17's fourth leg: health and backoff
// are observable, and a disabled indexer is actually left alone.
func TestIndexerFailureBackoff(t *testing.T) {
	// The startup-grace allowance is resolved BEFORE the scenario context
	// exists, so it is not charged against scenarioTimeout. It is an ALLOWANCE,
	// not a sleep: nothing here waits for it, the escalation wait below is
	// simply sized to survive it. See indexarrStartupGraceEndsAt.
	graceEndsAt := indexarrStartupGraceEndsAt(t)
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout+remaining(graceEndsAt))
	defer cancel()
	t0 := time.Now()

	// An indexer whose caps NEVER probe, asserted first and deliberately: it
	// documents why the escalation leg below does NOT use this personality.
	// indexarr computes escalation only where a failure is OBSERVED -- the RSS
	// poll and the search fan-out (R6/R15) -- the reconciler seeds the RSS
	// chain only for a healthy indexer, and the fan-out skips an indexer whose
	// status.caps is nil. So an indexer that cannot answer t=caps is never
	// polled and never queried, nothing calls RecordFailure, and its
	// escalationLevel stays 0 forever. It is NotReady, loudly, and that is all
	// it can ever be. (The D1-9 brief specified this personality for the
	// ladder; the tree says otherwise, and this is the tree's answer written
	// down.)
	dead := newIndexer(ctx, t, "e2e-idx-dead", torznabstub.PathDown, 15*time.Minute, true)
	var deadLive indexv1alpha1.Indexer
	waitFor(t, ctx, indexerReadyTimeout, "Indexer "+dead.Name+" NotReady with no caps",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dead), &deadLive); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return deadLive.Status.Caps == nil &&
				hasConditionFalse(deadLive.Status.Conditions, indexv1alpha1.IndexerConditionReady), nil
		}, describeIndexer(client.ObjectKeyFromObject(dead)), describeTorznabRequests(t0))
	require.Zero(t, deadLive.Status.EscalationLevel,
		"an indexer that never probed caps is never polled or queried, so nothing can escalate it")
	require.NotEmpty(t, torznabRequestsMatching(t, t0, torznabstub.PathDown, ""),
		"no request reached %s: spec.generic.apiPath was not joined onto spec.baseURL", torznabstub.PathDown)

	// The escalation driver: a personality that answers t=caps and fails every
	// SEARCH. It becomes Ready, so the reconciler seeds its RSS poll chain,
	// and then every poll fails against a real HTTP 500.
	broken := newIndexer(ctx, t, "e2e-idx-flaky", torznabstub.PathSearchDown, time.Minute, true)
	healthy := newIndexer(ctx, t, "e2e-idx-up", "", 15*time.Minute, false)
	waitForIndexerReady(ctx, t, healthy)

	// status.caps, NOT Ready, is what this indexer is waited on for -- and the
	// difference is a real race, not pedantry. Caps are folded in on a
	// SUCCESSFUL probe only and are never cleared by a later failure, so
	// "caps != nil" is monotonic; Ready/Healthy are not. Once indexarr is past
	// its startup grace the very first failing RSS poll disables this indexer
	// within seconds of the caps probe that made it Ready, so a wait on Ready
	// polling every 2s can miss the window entirely and hang -- which is
	// exactly what a rerun against an already-warm cluster would do. Caps
	// being present is also the thing the scenario actually depends on: it is
	// what makes the reconciler seed the RSS poll chain and what keeps the
	// search fan-out from skipping with "caps not probed".
	waitFor(t, ctx, indexerReadyTimeout, "Indexer "+broken.Name+" probed caps",
		func(ctx context.Context) (bool, error) {
			var live indexv1alpha1.Indexer
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(broken), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Status.Caps != nil, nil
		}, describeIndexer(client.ObjectKeyFromObject(broken)), describeTorznabRequests(t0))

	// Everything the search half needs, built now so its cost comes out of the
	// startup-grace wait rather than being added to it.
	profile := newRankedQualityProfile(ctx, t, "e2e-bo-qp", []tier{
		{name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
		{name: "Web720", qualities: []string{"WEBRip-720p", "WEBDL-720p"}},
	})
	rf := newRootFolder(ctx, t, "e2e-bo-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-bo-movie", fixtureSearchTmdbID, profile.Name, rf.Name,
		catalogv1alpha1.MinimumAvailabilityAnnounced)
	waitForMovieSettled(ctx, t, movie, "Fixture Search Film")

	// The RSS poll is the driver: the reconciler records no escalation (R6/R15
	// -- escalation is computed where the failure is observed), so a caps
	// failure alone would only move conditions.
	//
	// Level >= 2, not >= 1: the ladder's FIRST step is a zero-length disable
	// (indexarr/status.escalationTable starts [0, 1m, 5m, ...]), so level 1
	// buys only a 60-second window -- shorter than quietWindow, and the
	// indexer would legitimately come back mid-check. Level 2 is a 5-minute
	// window, which the quiet check fits inside with margin, and the final
	// assertion below proves the window really was still open.
	//
	// The deadline carries the startup-grace allowance on top of
	// escalationTimeout. It is added unconditionally rather than branched on
	// whether CLUSTARR_INDEXER_STARTUP_GRACE is set, because whether the
	// deployed indexarr HONOURS that variable is not observable from outside
	// the process -- and the only honest way to handle an unobservable fact is
	// not to depend on it. When the variable works, the condition holds within
	// a couple of minutes and the wait returns then; the allowance costs
	// nothing it does not need.
	escalationDeadline := escalationTimeout + remaining(graceEndsAt)
	if rem := remaining(graceEndsAt); rem > 0 {
		t.Logf("allowing an extra %s for indexarr's startup grace, which suppresses "+
			"escalation entirely until then", rem.Round(time.Second))
	}
	var down indexv1alpha1.Indexer
	waitFor(t, ctx, escalationDeadline, "Indexer "+broken.Name+" disabled by backoff",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(broken), &down); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return down.Status.EscalationLevel >= 2 &&
				down.Status.DisabledUntil != nil &&
				down.Status.DisabledUntil.After(time.Now()), nil
		}, describeIndexer(client.ObjectKeyFromObject(broken)), describeTorznabRequests(t0))

	require.NotNil(t, down.Status.InitialFailureAt,
		"the failure streak's anchor must be recorded and kept (correction C2): "+
			"an apply that omits it releases it, resetting the streak on every failure "+
			"and defeating the ladder entirely")
	require.NotNil(t, down.Status.LastFailureAt)
	require.NotEmpty(t, down.Status.LastFailure, "a failure must say what failed")
	require.False(t, isConditionTrue(down.Status.Conditions, indexv1alpha1.IndexerConditionHealthy))
	require.True(t, down.Status.DisabledUntil.After(down.Status.InitialFailureAt.Time))

	// It really was contacted, and really at the failing path: the only
	// assertion that spec.generic.apiPath is JOINED onto baseURL for a SEARCH.
	// If indexarr ignored it, every request would land on "/api" instead, the
	// indexer would be perfectly healthy, and this test would have failed on
	// the escalation wait with a request log full of successful /api calls --
	// a diagnosis, not a mystery.
	require.NotEmpty(t, torznabRequestsMatching(t, t0, torznabstub.PathSearchDown, "search"),
		"no RSS poll reached %s; %s", torznabstub.PathSearchDown, describeTorznabRequests(t0)())

	// Proof one, through the API: a fan-out that includes the disabled indexer
	// must SKIP it, not query it and fail. The Search is created only now,
	// after disabledUntil is set -- issued earlier it would legitimately report
	// "error" instead, and the assertion would be about timing rather than
	// about the gate.
	done := runSearch(ctx, t, movie, []string{broken.Name, healthy.Name})

	byName := map[string]catalogv1alpha1.IndexerOutcome{}
	for _, o := range done.Status.IndexerOutcomes {
		byName[o.Name] = o
	}
	require.Contains(t, byName, broken.Name)
	require.Contains(t, byName, healthy.Name)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeSkipped, byName[broken.Name].State,
		"a disabled indexer must be SKIPPED, not queried and failed: "+
			"an 'error' outcome here means indexer health was never consulted")
	require.Zero(t, byName[broken.Name].Count)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, byName[healthy.Name].State,
		"one indexer's backoff must not take the healthy one down with it; error=%q",
		byName[healthy.Name].Error)
	require.NotEmpty(t, done.Status.Results, "the healthy indexer still contributed results")

	// Proof two, through the file channel: the fixture sees no further QUERY
	// on the failing path. A worker ignoring the backoff would poll within one
	// rssInterval; quietWindow is two, so a delivery that slipped cannot
	// manufacture a false pass by being late.
	//
	// t=caps is excluded deliberately. The reconciler's own re-probe is a
	// health check, not a query -- it is how an indexer is ever found healthy
	// again -- and the backoff's contract is that SEARCHES stop.
	mark := time.Now()
	waitUntil(ctx, t, mark.Add(quietWindow), "the disabled indexer's quiet window")
	var after []torznabstub.Entry
	for _, r := range torznabRequestsMatching(t, mark, torznabstub.PathSearchDown, "") {
		if r.T != "caps" {
			after = append(after, r)
		}
	}
	require.Empty(t, after,
		"the disabled indexer was queried %d more time(s) during its backoff window: %+v",
		len(after), after)

	// ...and the window really was still open for the whole of it, so the
	// quiet above was the gate and not an expired disable.
	var stillDown indexv1alpha1.Indexer
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(broken), &stillDown))
	require.NotNil(t, stillDown.Status.DisabledUntil)
	require.True(t, stillDown.Status.DisabledUntil.After(time.Now()),
		"the backoff window expired during the quiet check; the assertion proved nothing")
}

// hasConditionFalse is isConditionTrue's negative twin, kept separate because
// "absent" must not read as "False": an Indexer whose Ready condition has not
// been written yet is not the same as one the controller has judged NotReady.
func hasConditionFalse(conds []metav1.Condition, condType string) bool {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status == metav1.ConditionFalse
		}
	}
	return false
}

// waitUntil sleeps until at, in context-respecting slices, logging once so a
// long pause in the output is explained rather than mistaken for a hang.
func waitUntil(ctx context.Context, t *testing.T, at time.Time, why string) {
	t.Helper()
	d := time.Until(at)
	if d <= 0 {
		return
	}
	t.Logf("waiting %s for %s", d.Round(time.Second), why)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatalf("context expired while waiting %s for %s", d.Round(time.Second), why)
	}
}

// remaining is how long is left until at, never negative.
func remaining(at time.Time) time.Duration {
	if d := time.Until(at); d > 0 {
		return d
	}
	return 0
}

// indexarrStartupGraceEndsAt returns the wall-clock instant from which
// indexarr's backoff ladder is certain to be able to move.
//
// indexarr/status.RecordFailure suppresses escalation for any failure inside
// StartupGrace (15 minutes) of the indexarr PROCESS's start -- Prowlarr's
// MinimumTimeSinceStartup, so a restart does not disable every indexer at
// once. `make e2e` has a 30-minute budget in total, so that window is half of
// it, and how old indexarr's process is when this scenario runs depends
// entirely on how long the scenarios before it took.
//
// config/e2e/indexarr-e2e-patch.yaml therefore sets
// CLUSTARR_INDEXER_STARTUP_GRACE=0s. THAT VARIABLE IS NOT READ BY INDEXARR
// YET: StartupGrace is a const in indexarr/status/health.go and nothing in
// indexarr/run.go plumbs an override. The D1-9 brief listed the variable as an
// interface task D1-8 would provide; D1-8 did not, and D1-9 may not edit
// indexarr.
//
// Whether the deployed image honours the variable is NOT observable from
// outside the process, so this function does not branch on it. It checks that
// config/e2e declares the variable correctly when it declares it at all --
// that much is checkable -- and otherwise always returns the instant the real
// grace elapses, measured from the newest indexarr Pod's startTime. The caller
// uses it as an ALLOWANCE on a wait's deadline, never as a sleep: when the
// variable does work, the ladder moves in a couple of minutes and the wait
// returns then, so the allowance costs nothing. Plumb the variable in
// indexarr/run.go and the worst case disappears too.
//
// The margin covers the gap between the Pod's StartTime (which the kubelet
// stamps) and the moment the process's own package variables initialise.
func indexarrStartupGraceEndsAt(t *testing.T) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var dep appsv1.Deployment
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "indexarr"}, &dep),
		"the indexarr Deployment must exist; TestMain's gate should already have refused otherwise")

	const graceEnv = "CLUSTARR_INDEXER_STARTUP_GRACE"
	declared := false
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name != graceEnv {
				continue
			}
			declared = true
			d, err := time.ParseDuration(e.Value)
			require.NoError(t, err, "%s=%q is not a Go duration", graceEnv, e.Value)
			require.Zero(t, d, "config/e2e must set %s to 0s, not %q", graceEnv, e.Value)
		}
	}

	// The real grace, measured from the newest indexarr Pod -- the newest,
	// because that is the process whose clock the ladder is compared against.
	var pods corev1.PodList
	require.NoError(t, k8sClient.List(ctx, &pods,
		client.InNamespace(Namespace),
		client.MatchingLabels{"app.kubernetes.io/component": "indexarr"}))
	var newest time.Time
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.StartTime != nil && p.Status.StartTime.After(newest) {
			newest = p.Status.StartTime.Time
		}
	}
	require.False(t, newest.IsZero(),
		"no running indexarr Pod reported a startTime, so its startup grace cannot be bounded")

	const startMargin = 30 * time.Second
	endsAt := newest.Add(idxstatus.StartupGrace).Add(startMargin)
	t.Logf("indexarr Pod started %s; %s declared in the Deployment: %t. "+
		"Allowing until %s (%s + %s margin) for escalation to become possible; the wait "+
		"returns as soon as the ladder actually moves, which is immediately if the variable "+
		"is honoured. Plumb it in indexarr/run.go to remove the worst case.",
		newest.Format(time.RFC3339), graceEnv, declared,
		endsAt.Format(time.RFC3339), idxstatus.StartupGrace, startMargin)
	return endsAt
}
