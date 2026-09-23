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

package search_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// targetCapture is an EvaluateFunc seam that records the decision.Target the
// worker assembled. The snapshot has no exported surface of its own -- it is
// an implementation detail of Handle -- so this is how its contents are
// asserted, against the real flow rather than a reimplementation of it.
type targetCapture struct {
	mu     sync.Mutex
	target decision.Target
	opts   decision.Options
	rels   []commonv1.ReleaseInfo
	called bool
}

func (c *targetCapture) evaluate(_ context.Context, t decision.Target, _ quality.Profile, _ *catalogue.Catalogue, rels []commonv1.ReleaseInfo, o decision.Options) []decision.Decision {
	c.mu.Lock()
	c.target, c.opts, c.rels, c.called = t, o, rels, true
	c.mu.Unlock()
	return nil
}

func (c *targetCapture) get(t *testing.T) (decision.Target, decision.Options, []commonv1.ReleaseInfo) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.True(t, c.called, "Evaluate was never reached")
	return c.target, c.opts, c.rels
}

func TestWorkerSnapshotOfAnAnimeEpisodeWithAFile(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	const ns = "snapshot-episode"
	newNamespace(t, ctx, c, ns)

	qp := testQualityProfile("snap-episode")
	require.NoError(t, c.Create(ctx, qp))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "one-piece", Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 81797, SeriesType: catalogv1alpha1.SeriesTypeAnime,
			QualityProfileRef: qp.Name, RootFolderRef: "tv",
		},
	}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "one-piece-s01e37", Namespace: ns},
		Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef: "one-piece", SeasonNumber: 1, EpisodeNumber: 37,
		},
	}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "one-piece-s01e37-file", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "one-piece-s01e37"},
			Path:     "/data/tv/One Piece/Season 01/One Piece - S01E37.mkv",
			Quality:  commonv1.Quality{Name: "WEBDL-1080p", Source: "webdl", Resolution: 1080},
			Revision: commonv1.Revision{Version: 1},
			// FormatScore lives on spec, frozen at import (§8.4); there is
			// no status equivalent.
			FormatScore:    55,
			MatchedFormats: []string{"x265"},
			ImportedFrom: &catalogv1alpha1.ImportSource{
				DownloadRef:  "op-download",
				ReleaseTitle: "One.Piece.S01E37.1080p.WEB-DL.x265-OLD",
			},
		},
	}))
	newDownload(t, ctx, c, ns, downloadFixture{
		name: "op-download", hash: "cccccccccccccccccccccccccccccccccccccccc",
		title:  "One.Piece.S01E37.1080p.WEB-DL.x265-OLD",
		target: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "one-piece-s01e37"},
		phase:  downloadv1alpha1.DownloadPhaseSeeding,
	})

	airDate := metav1.NewTime(time.Date(2000, 6, 4, 0, 0, 0, 0, time.UTC))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		catalogac.Episode("one-piece-s01e37", ns).WithStatus(
			catalogac.EpisodeStatus().
				WithTvdbID(4242).
				WithAirDate(airDate).
				WithRuntimeMinutes(24).
				WithAbsoluteNumber(37).
				WithPhase(catalogv1alpha1.EpisodePhaseCutoffUnmet).
				WithHasFile(true).
				WithFileRef("one-piece-s01e37-file")))
	require.NoError(t, err)

	capture := &targetCapture{}
	rpc := &search.FakeSearchRPC{Response: schema.SearchResponse{
		Releases: []schema.Release{rpcRelease("g1", "One.Piece.E37.1080p.WEB-DL.x264-NEW", 90)},
	}}
	w := search.NewWorker(c, rpc, catalogue.LoadedCatalogue())
	w.Evaluate = capture.evaluate
	w.Sink = newRecordingSink()
	w.Clock = clockwork.NewFakeClockAt(testNow)

	waitCached(t, ctx, c, client.ObjectKey{Namespace: ns, Name: "one-piece-s01e37-file"}, &catalogv1alpha1.MediaFile{})
	eventually(t, 10*time.Second, "the episode status to reach the cache", func() bool {
		var e catalogv1alpha1.Episode
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "one-piece-s01e37"}, &e); err != nil {
			return false
		}
		return e.Status.HasFile
	})

	schemaName, data, err := schema.Encode(schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "one-piece-s01e37"},
		Reason:   schema.SearchReasonCutoffUnmet,
	})
	require.NoError(t, err)
	require.NoError(t, w.Handle(ctx, testMessage{env: &events.Envelope{
		ID: "snap-1", Type: "catalog.SearchTask", Schema: schemaName,
		Key: ns + "/one-piece-s01e37", Time: time.Now(), Data: data,
	}}))

	target, opts, rels := capture.get(t)

	require.Equal(t, commonv1.MediaKindEpisode, target.Kind)
	require.Equal(t, events.MediaKey(string(commonv1.MediaKindEpisode), ns, "one-piece-s01e37"), target.Key)
	require.True(t, target.Monitored, "spec.monitored defaults to true")
	require.True(t, target.Available, "an episode that aired in 2000 is available")
	require.Equal(t, []int{24}, target.EpisodeRuntimes,
		"pkg/decision reads an episode's runtime from EpisodeRuntimes, not RuntimeMinutes")
	require.Equal(t, 24, target.RuntimeMinutes)

	require.NotNil(t, target.Current, "the item has a MediaFile")
	require.Equal(t, "WEBDL-1080p", target.Current.Quality.Name)
	require.Equal(t, 55, target.Current.FormatScore)
	require.Equal(t, []string{"x265"}, target.Current.Formats)
	require.Equal(t, "One.Piece.S01E37.1080p.WEB-DL.x265-OLD", target.Current.SourceTitle)
	require.Equal(t, "cccccccccccccccccccccccccccccccccccccccc", target.Current.SourceHash,
		"the info hash comes from the Download that produced the file, not from MediaFile")

	// The identity the decision engine checks every release against. The
	// series has no metadata in this fixture, so there is no title yet: the
	// SERIES tvdb id and the episode's own numbering are what identify it.
	airedOn := airDate.Time
	require.Equal(t, decision.Identity{
		IDs:    map[string]string{commonv1.IDKeyTVDB: "81797"},
		Season: 1, Episodes: []int{37}, Absolute: []int{37}, AirDate: &airedOn,
		IDQueryIndexers: map[string]bool{}, // the fake reply reports no id-mode outcome
	}, target.Identity)

	require.Len(t, target.Queue, 1, "a seeding Download still occupies the queue")
	require.NotNil(t, target.Blocklist)
	require.False(t, target.Blocklist("deadbeef", "Nothing.Blocklisted"), "nothing is blocklisted here")
	require.Empty(t, opts.ProtocolsEnabled,
		"an AUTOMATIC search fails closed when no DownloadClient exists: its ranked list goes straight to a grab")
	require.False(t, opts.UserInvoked)

	require.Len(t, rels, 1)
	// A release the indexer gave no pubDate for now arrives as a genuine nil
	// rather than a manufactured date. It used to be backfilled because a
	// non-pointer metav1.Time could not be persisted at all; PublishedAt is a
	// pointer now, so absence survives, and pkg/decision scores an unknown
	// date neutrally instead of ranking it as brand new.
	require.Nil(t, rels[0].PublishedAt,
		"an absent publish date must pass through unmodified, not be backfilled")

	reqs := rpc.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, "81797", reqs[0].IDs[commonv1.IDKeyTVDB],
		"a tv-search is keyed by the SERIES tvdb id, never the episode's own")
	require.Nil(t, reqs[0].Season, "anime uses absolute numbering with no season token")
	require.NotNil(t, reqs[0].Episode)
	require.Equal(t, int32(37), *reqs[0].Episode, "the absolute number goes on the wire")
	require.Equal(t, []int32{5000}, reqs[0].Categories)
}

func TestWorkerSnapshotOfAMovieWithNoFileHasNoCurrent(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "snapshot-movie")

	capture := &targetCapture{}
	f.worker.Evaluate = capture.evaluate

	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonAdd,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	target, _, _ := capture.get(t)
	require.Equal(t, commonv1.MediaKindMovie, target.Kind)
	require.Nil(t, target.Current, "a movie with no MediaFile has no current candidate")
	require.Empty(t, target.Queue)
	require.False(t, target.Available, "status.available is false until the Movie reconciler says otherwise")
	require.True(t, target.Monitored)
	require.Equal(t, map[string]string{commonv1.IDKeyTMDB: "603"}, target.Identity.IDs,
		"with no metadata yet the movie is still identifiable by its spec tmdb id")
	require.Empty(t, target.Identity.Titles)
}

func TestWorkerBlocklistPredicateHonoursTheExpiryDeadline(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "snapshot-blocklist")

	// Derived from testNow, the instant the worker's clock is frozen at --
	// not from time.Now(), which is a different clock and drifts away from it
	// by one day per day.
	live := metav1.NewTime(testNow.Add(24 * time.Hour))
	expired := metav1.NewTime(testNow.Add(-24 * time.Hour))
	newDownload(t, ctx, f.mgr, f.ns, downloadFixture{
		name: "blocked-live", hash: "1111111111111111111111111111111111111111",
		title: "The.Matrix.1999.1080p.BluRay.x264-BANNED", target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		blocklisted: true, blocklistedUntil: &live, phase: downloadv1alpha1.DownloadPhaseBlocklisted,
	})
	newDownload(t, ctx, f.mgr, f.ns, downloadFixture{
		name: "blocked-expired", hash: "2222222222222222222222222222222222222222",
		title: "The.Matrix.1999.720p.HDTV.x264-STALE", target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		blocklisted: true, blocklistedUntil: &expired, phase: downloadv1alpha1.DownloadPhaseBlocklisted,
	})
	eventually(t, 10*time.Second, "both blocklist entries to reach the cache", func() bool {
		var list downloadv1alpha1.DownloadList
		if err := f.mgr.List(ctx, &list, client.InNamespace(f.ns),
			client.MatchingFields{search.IndexBlocklistInfoHash: "2222222222222222222222222222222222222222"}); err != nil {
			return false
		}
		return len(list.Items) == 1
	})

	capture := &targetCapture{}
	f.worker.Evaluate = capture.evaluate
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})}))

	target, _, _ := capture.get(t)
	require.NotNil(t, target.Blocklist)
	require.True(t, target.Blocklist("1111111111111111111111111111111111111111", "anything"),
		"a live blocklist entry matches by info hash")
	require.True(t, target.Blocklist("", "The.Matrix.1999.1080p.BluRay.x264-BANNED"),
		"a usenet release with no info hash matches by normalized title")
	require.True(t, target.Blocklist("1111111111111111111111111111111111111111", "unrelated"),
		"the hash is matched case-insensitively and independently of the title")
	require.False(t, target.Blocklist("2222222222222222222222222222222222222222", "unrelated"),
		"an expired blocklist entry no longer blocks; expiry is applied at read time, not baked into the index")
	require.False(t, target.Blocklist("3333333333333333333333333333333333333333", "Never.Seen.Before"))
}

// TestWorkerSearchAtCRDDefaultsApprovesAnEnglishRelease is the search path's
// half of the language-vocabulary regression, and the only test in this
// package that runs the REAL pkg/decision.Evaluate: every other one replaces
// it with approveEverything or a capture, which is exactly why a search that
// approved nothing at all could ship.
//
// The Movie's originalLanguage is "en" -- a BCP-47 tag, which is what
// MovieMetadata.OriginalLanguage is documented to hold and what the metadata
// gateway writes -- and testQualityProfile sets neither language nor
// minFormatScore, so the apiserver defaults them to "original" and 0. Before
// the boundary conversion in pkg/decision/language.go, that combination
// rejected this English release twice over: once as ReasonWantedLanguage
// ("en" never matches ["English"]) and once as a -10000 custom-format score
// from language-not-original.
//
// The search is INTERACTIVE only because an automatic one fails closed on
// protocols without a DelayProfile (TestWorkerProtocolFallbackIsGatedOnUserInvoked),
// which would mask the language verdict behind a ProtocolDisabled rejection.
// Every check this test is about -- language, custom-format score, quality,
// size -- runs identically on both paths.
func TestWorkerSearchAtCRDDefaultsApprovesAnEnglishRelease(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "snapshot-crd-defaults")
	f.worker.Evaluate = decision.Evaluate

	_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogarrMetadata,
		catalogac.Movie("the-matrix", f.ns).WithStatus(
			catalogac.MovieStatus().WithAvailable(true).WithMetadata(
				catalogac.MovieMetadata().WithTitle("The Matrix").WithYear(1999).
					WithRuntimeMinutes(136).
					WithOriginalLanguage("en").
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	eventually(t, 10*time.Second, "the cache to see the movie's metadata", func() bool {
		var m catalogv1alpha1.Movie
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m); err != nil {
			return false
		}
		return m.Status.Metadata != nil && m.Status.Metadata.OriginalLanguage == "en"
	})

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	writeControllerStatus(t, ctx, f.mgr, f.ns, "srch")

	env := f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch"},
		UserInvoked: true,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch"}, got))
	require.Len(t, got.Status.Results, 2)
	for _, r := range got.Status.Results {
		require.True(t, r.Approved,
			"a profile at CRD defaults must approve an English release of an English movie; %s was rejected with %+v (score %d)",
			r.GUID, r.Rejections, r.FormatScore)
	}
}

// TestWorkerSearchRejectsAWrongFilmFromATextFallbackIndexer is G1-6b through
// the worker, with the REAL pkg/decision.Evaluate: the identity the worker
// assembles (titles and ids from the Movie, IDQueryIndexers from the reply's
// per-indexer QueryMode) is what decides which releases are for this film.
//
// Two indexers answer. "text-idx" supported none of the movie's ids and fell
// back to "The Matrix 1999"; "id-idx" answered a tmdbid query. The right film
// is approved from the text indexer by its title and year; a different film
// from the same keyword search is rejected as WrongItem; and a release titled
// in another language, with no ids of its own, is approved from the id
// indexer -- which matched the id server-side -- and rejected from the text
// indexer, which only matched a keyword.
func TestWorkerSearchRejectsAWrongFilmFromATextFallbackIndexer(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "snapshot-identity")
	f.worker.Evaluate = decision.Evaluate

	_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogarrMetadata,
		catalogac.Movie("the-matrix", f.ns).WithStatus(
			catalogac.MovieStatus().WithAvailable(true).WithMetadata(
				catalogac.MovieMetadata().WithTitle("The Matrix").WithYear(1999).
					WithRuntimeMinutes(136).
					WithOriginalLanguage("en").
					WithExternalIDs(map[string]string{commonv1.IDKeyIMDB: "tt0133093"}).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	eventually(t, 10*time.Second, "the cache to see the movie's metadata", func() bool {
		var m catalogv1alpha1.Movie
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m); err != nil {
			return false
		}
		return m.Status.Metadata != nil && m.Status.Metadata.Title == "The Matrix"
	})

	from := func(indexer, guid, title string) schema.Release {
		r := rpcRelease(guid, title, 0)
		r.Info.IndexerRef, r.Info.IndexerName = indexer, indexer
		return r
	}
	f.rpc.Response = schema.SearchResponse{
		Releases: []schema.Release{
			from("text-idx", "right", "The.Matrix.1999.1080p.BluRay.x264-GRP"),
			from("text-idx", "sequel", "The.Matrix.Reloaded.2003.1080p.BluRay.x264-GRP"),
			from("text-idx", "foreign-by-text", "Matriks.1999.1080p.BluRay.x264-GRP"),
			from("id-idx", "foreign-by-id", "Matriks.1999.1080p.BluRay.x264-GRP"),
		},
		Outcomes: []schema.SearchOutcome{
			{IndexerRef: schema.Ref{Namespace: f.ns, Name: "text-idx"}, Status: schema.SearchOutcomeOK, Releases: 3, QueryMode: schema.SearchQueryModeText},
			{IndexerRef: schema.Ref{Namespace: f.ns, Name: "id-idx"}, Status: schema.SearchOutcomeOK, Releases: 1, QueryMode: schema.SearchQueryModeID},
		},
	}

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	writeControllerStatus(t, ctx, f.mgr, f.ns, "srch")

	// Interactive only so protocols open without a DownloadClient (see
	// TestWorkerSearchAtCRDDefaultsApprovesAnEnglishRelease); identity is
	// checked identically on both paths.
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch"},
		UserInvoked: true,
	})}))

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch"}, got))
	byGUID := map[string]catalogv1alpha1.ReleaseDecision{}
	for _, r := range got.Status.Results {
		byGUID[r.GUID] = r
	}
	require.Len(t, byGUID, 4)

	for _, guid := range []string{"right", "foreign-by-id"} {
		require.True(t, byGUID[guid].Approved, "%s must be approved; rejected with %+v", guid, byGUID[guid].Rejections)
	}
	for _, guid := range []string{"sequel", "foreign-by-text"} {
		r := byGUID[guid]
		require.False(t, r.Approved, "%s is not this film and must not be approved", guid)
		require.Len(t, r.Rejections, 1, "identity must be the only reason %s is rejected: %+v", guid, r.Rejections)
		require.Contains(t, r.Rejections[0].Reason, decision.ReasonWrongItem.Code+":")
	}
}
