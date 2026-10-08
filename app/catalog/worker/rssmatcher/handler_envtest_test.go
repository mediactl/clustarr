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

package rssmatcher_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// testMessage is the minimal events.Message Handle uses: it only reads
// Envelope(). Delivery mechanics are membus's and pkg/events' own contract
// tests.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

func releaseMessage(t *testing.T, ns, indexer string, rel schema.Release) events.Message {
	t.Helper()
	schemaName, data, err := schema.Encode(rel)
	require.NoError(t, err)
	return testMessage{env: &events.Envelope{
		Type: "index.Release", Schema: schemaName, Key: ns + "/" + indexer, Time: relNow, Data: data,
	}}
}

// recordingBus keeps every Candidate the matcher sends to an owner's
// intake -- the matcher's whole output since A4.4 (ADR-0019 §6.6 step 1):
// it creates no Download and writes nothing; the owner's grab planner
// re-checks, delays, narrows and grabs. An intake publish is recorded and
// not forwarded; anything else goes to the membus underneath.
type recordingBus struct {
	events.Bus
	mu    sync.Mutex
	cands []schema.Candidate
}

func newRecordingBus(t *testing.T) *recordingBus { return &recordingBus{Bus: newTestBus(t)} }

func (b *recordingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	if !strings.HasPrefix(subject, "clustarr.intake.candidate.") {
		return b.Bus.Publish(ctx, subject, e, opts...)
	}
	var c schema.Candidate
	if err := schema.Decode(e.Schema, e.Data, &c); err != nil {
		return events.Receipt{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cands = append(b.cands, c)
	return events.Receipt{Seq: uint64(len(b.cands))}, nil
}

func (b *recordingBus) candidates() []schema.Candidate {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]schema.Candidate(nil), b.cands...)
}

// blurayRelease is a top-tier hd-bluray-web candidate for the seeded movie.
func blurayRelease(tmdbID string) schema.Release {
	rel := movieRelease("The Thing", 1982, map[string]string{commonv1.IDKeyTMDB: tmdbID})
	rel.Info.Quality = commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}
	return rel
}

// TestHandler_ApprovedReleaseTakesTheGrabPath is §8.7's clause end to end: a
// matched, approved release is sent to its owner as an RSS Candidate -- the
// path a search's pick takes too -- and the matcher writes nothing: no
// Download (there is no Download kind), no activeDownloadRef, no phase.
func TestHandler_ApprovedReleaseTakesTheGrabPath(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	movie := createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true) // no delay at all: grab now

	bus := newRecordingBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})

	// The manager's cache is eventually consistent, so poll until the write
	// is visible to the index rather than racing it.
	eventually(t, 15*time.Second, "the release to be matched and sent", func() bool {
		if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", blurayRelease("1091"))); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		return len(bus.candidates()) > 0
	})

	cand := bus.candidates()[0]
	assert.Equal(t, commonv1.GrabSourceRSS, cand.GrabbedBy, "an RSS grab must be attributed to rss")
	assert.Equal(t, schema.CandidateOriginRSS, cand.Origin)
	assert.Equal(t, "guid-1", cand.Release.Decision.GUID)
	assert.Equal(t, "Movie", cand.Owner.Kind)
	assert.Equal(t, movie.Name, cand.Owner.Name)

	// Read uncached: managedFields is what shows an over-claim.
	var got catalogv1alpha1.Movie
	require.NoError(t, mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.ActiveDownloadRef, "the matcher never writes activeDownloadRef")
	for _, mf := range got.ManagedFields {
		if mf.Manager == string(k8s.ManagerCatalogGrab) && mf.FieldsV1 != nil {
			assert.NotContains(t, mf.FieldsV1.GetRawString(), `"f:activeDownloadRef"`,
				"the grab manager must not claim activeDownloadRef")
			assert.NotContains(t, mf.FieldsV1.GetRawString(), `"f:phase"`, "the RSS path must never write Phase")
		}
	}
	assert.Empty(t, got.Status.Phase, "the RSS path must never write Phase")
	require.NotNil(t, got.Status.Metadata, "the matcher must not release the gateway's status.metadata")
}

// decisionCapture wraps the REAL decision.Evaluate and records what it was
// given and what it decided, so a test can assert on the verdict itself
// rather than infer it from a Download that did or did not appear. With
// suppressGrab set it hands the handler back its decisions with Approved
// cleared, so a test about the decision does not also exercise the grab path,
// which the tests above cover.
type decisionCapture struct {
	mu           sync.Mutex
	suppressGrab bool
	target       decision.Target
	decisions    []decision.Decision
}

func (c *decisionCapture) evaluate(ctx context.Context, t decision.Target, p quality.Profile, cat *catalogue.Catalogue, rels []commonv1.ReleaseInfo, o decision.Options) []decision.Decision {
	ds := decision.Evaluate(ctx, t, p, cat, rels, o)
	c.mu.Lock()
	c.target, c.decisions = t, append([]decision.Decision(nil), ds...)
	c.mu.Unlock()
	if !c.suppressGrab {
		return ds
	}
	out := append([]decision.Decision(nil), ds...)
	for i := range out {
		out[i].Approved = false
	}
	return out
}

func (c *decisionCapture) get() (decision.Target, []decision.Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target, c.decisions
}

// TestHandler_ConflictingIDIsRejectedEvenWhenTheTitleMatches is the RSS half
// of G1-6b. The release names tmdb 841, which no catalog movie has, so the
// matcher falls back to title and year and matches "The Thing (1982)" --
// tmdb 1091. Before the identity check nothing compared the release's own id
// with the item it had been title-matched to, and this release was grabbed
// for the wrong film.
func TestHandler_ConflictingIDIsRejectedEvenWhenTheTitleMatches(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true) // no delay: an approval would grab at once

	rel := blurayRelease("841")
	// Gate on the title index, or "nothing was grabbed" could just mean
	// "nothing was matched yet".
	eventually(t, 15*time.Second, "the title-and-year index to match the release", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		return err == nil && len(refs) == 1 && refs[0].Name == "the-thing-1982"
	})

	capture := &decisionCapture{}
	bus := newRecordingBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Evaluate: capture.evaluate, Now: func() time.Time { return relNow }})
	require.NoError(t, h.Handle(ctx, releaseMessage(t, ns, "my-indexer", rel)))

	target, ds := capture.get()
	require.Equal(t, "1091", target.Identity.IDs[commonv1.IDKeyTMDB], "the matched movie's identity reached the decision")
	require.Len(t, ds, 1)
	require.False(t, ds[0].Approved)
	require.Len(t, ds[0].Rejections, 1, "identity is the only thing wrong with this release: %+v", ds[0].Rejections)
	assert.True(t, strings.HasPrefix(ds[0].Rejections[0].Reason, decision.ReasonWrongItem.Code+":"), ds[0].Rejections[0].Reason)
	assert.Contains(t, ds[0].Rejections[0].Reason, "release tmdb id 841 conflicts with the item's 1091")

	assert.Empty(t, bus.candidates(), "a release for another film must not be sent to its owner")
}

// TestHandler_EpisodeAndPackTargetsCarryTheirNumbering proves resolve hands
// the decision engine an episode's numbering for BOTH target shapes the
// matcher produces. The pack shape is the one that could silently break: its
// numbering is not on the Series at all but on the Episodes named in Keys,
// and a pack target without it fails closed as UnknownItem -- every RSS
// season pack would stop being grabbed, with nothing louder than a metric.
func TestHandler_EpisodeAndPackTargetsCarryTheirNumbering(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "the-wire", 79126, "The Wire", 2002)
	aired := relNow.Add(-30 * 24 * time.Hour)
	for n := int32(1); n <= 3; n++ {
		createEpisode(t, ctx, c, ns, "the-wire", 1, n, &aired)
	}
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true)

	tvRelease := func(title string, kind commonv1.MediaKind, episodes []int32, fullSeason bool) schema.Release {
		return schema.Release{
			Info: commonv1.ReleaseInfo{
				GUID: "guid-" + title, IndexerRef: "my-indexer", IndexerName: "my-indexer",
				Protocol: commonv1.ProtocolTorrent, Title: title,
				MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
				PublishedAt: metaTime(relNow.Add(-time.Hour)),
				IDs:         map[string]string{commonv1.IDKeyTVDB: "79126"},
			},
			ParsedTitle: "The Wire", Kind: kind, Seasons: []int32{1}, Episodes: episodes, FullSeason: fullSeason,
			FetchedAt: relNow,
		}
	}

	cases := []struct {
		name         string
		rel          schema.Release
		wantKind     commonv1.MediaKind
		wantEpisodes []int
	}{
		{"a single episode", tvRelease("The.Wire.S01E02.1080p.BluRay.x264-GROUP", commonv1.MediaKindEpisode, []int32{2}, false), commonv1.MediaKindEpisode, []int{2}},
		{"a season pack", tvRelease("The.Wire.S01.1080p.BluRay.x264-GROUP", commonv1.MediaKindSeries, nil, true), commonv1.MediaKindSeries, []int{1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &decisionCapture{suppressGrab: true}
			h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: newTestBus(t), Evaluate: capture.evaluate, Now: func() time.Time { return relNow }})
			eventually(t, 15*time.Second, "every episode to be matched and decided", func() bool {
				if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", tc.rel)); err != nil {
					t.Fatalf("Handle: %v", err)
				}
				target, ds := capture.get()
				return len(ds) == 1 && len(target.Identity.Episodes) == len(tc.wantEpisodes)
			})

			target, ds := capture.get()
			assert.Equal(t, tc.wantKind, target.Kind)
			assert.Equal(t, []string{"The Wire"}, target.Identity.Titles)
			assert.Equal(t, "79126", target.Identity.IDs[commonv1.IDKeyTVDB])
			assert.Equal(t, 1, target.Identity.Season)
			assert.Equal(t, tc.wantEpisodes, target.Identity.Episodes)
			assert.Nil(t, target.Identity.IDQueryIndexers, "a firehose release was found by no query")
			require.True(t, ds[0].Approved, "the release covers its target and must be approved; rejected with %+v", ds[0].Rejections)
		})
	}
}

// fakeSceneSource is a scenemap.Source over literal tables.
type fakeSceneSource map[int64]*scenemap.Map

func (f fakeSceneSource) SceneMap(_ context.Context, tvdbID int64) (*scenemap.Map, error) {
	if m, ok := f[tvdbID]; ok {
		return m, nil
	}
	return &scenemap.Map{TVDBID: tvdbID}, nil
}

// TestHandler_SceneTableReachesTheIdentity: the RSS decision reads a release
// through the series' whole TheXEM table, the same one the search worker
// hands its decision, and it is never a single-episode search (ruling R-3
// binds the search path only).
func TestHandler_SceneTableReachesTheIdentity(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "the-wire", 79126, "The Wire", 2002)
	aired := relNow.Add(-30 * 24 * time.Hour)
	createEpisode(t, ctx, c, ns, "the-wire", 1, 2, &aired)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true)

	xem := &scenemap.Map{TVDBID: 79126, Mappings: []scenemap.Mapping{
		{Scene: scenemap.Numbering{Season: 1, Episode: 2}, TVDB: scenemap.Numbering{Season: 1, Episode: 2}},
		{Scene: scenemap.Numbering{Season: 2, Episode: 1}, TVDB: scenemap.Numbering{Season: 1, Episode: 13}},
	}}
	capture := &decisionCapture{suppressGrab: true}
	h := rssmatcher.NewHandler(rssmatcher.Deps{
		Client: c, Bus: newTestBus(t), Evaluate: capture.evaluate, SceneMaps: fakeSceneSource{79126: xem},
		Now: func() time.Time { return relNow },
	})
	rel := blurayEpisode()
	eventually(t, 15*time.Second, "the episode to be matched and decided", func() bool {
		if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", rel)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		_, ds := capture.get()
		return len(ds) == 1
	})
	target, _ := capture.get()
	assert.Equal(t, []decision.SceneMapping{
		{Scene: decision.EpisodeNumbering{Season: 1, Episode: 2}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 2}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 1}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13}},
	}, target.Identity.SceneMappings, "the whole table, not only the matched episode's row")
	assert.False(t, target.Identity.SingleEpisodeSearch)
}

// blurayEpisode is The Wire S01E02 in Bluray-1080p.
func blurayEpisode() schema.Release {
	return schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: "guid-e02", IndexerRef: "my-indexer", IndexerName: "my-indexer",
			Protocol: commonv1.ProtocolTorrent, Title: "The.Wire.S01E02.1080p.BluRay.x264-GROUP",
			MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
			PublishedAt: metaTime(relNow.Add(-time.Hour)),
			IDs:         map[string]string{commonv1.IDKeyTVDB: "79126"},
		},
		ParsedTitle: "The Wire", Kind: commonv1.MediaKindEpisode, Seasons: []int32{1}, Episodes: []int32{2},
		FetchedAt: relNow,
	}
}

// TestHandler_UnmatchedReleaseIsAcknowledged: most of the firehose matches
// nothing, and that path must ack without a single write.
func TestHandler_UnmatchedReleaseIsAcknowledged(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	bus := newRecordingBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})
	require.NoError(t, h.Handle(ctx, releaseMessage(t, ns, "my-indexer", blurayRelease("999999"))))
	assert.Empty(t, bus.candidates())
}

func TestHandler_MalformedMessagesAreDiscarded(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: newTestBus(t), Now: func() time.Time { return relNow }})

	t.Run("wrong schema", func(t *testing.T) {
		err := h.Handle(ctx, testMessage{env: &events.Envelope{Schema: "catalog.GrabTask.v1", Key: "media/x", Data: []byte(`{}`)}})
		var discard *events.DiscardError
		require.ErrorAs(t, err, &discard)
	})
	t.Run("key without a namespace", func(t *testing.T) {
		msg := releaseMessage(t, "media", "my-indexer", blurayRelease("1"))
		msg.Envelope().Key = "no-slash"
		var discard *events.DiscardError
		require.ErrorAs(t, h.Handle(ctx, msg), &discard)
	})
}

// TestHandler_SubscriptionMatchesTheSpecTable pins §5's
// catalog-rss-matcher row so a change to the shared topology cannot
// silently retune this consumer.
func TestHandler_SubscriptionMatchesTheSpecTable(t *testing.T) {
	sub := rssmatcher.NewHandler(rssmatcher.Deps{}).Subscription()
	require.NoError(t, sub.Validate())
	assert.Equal(t, events.ConsumerCatalogRSSMatcher, sub.Durable)
	assert.Equal(t, []string{events.FilterAllReleases}, sub.Filters)
	assert.Equal(t, 30*time.Second, sub.AckWait)
	assert.Equal(t, 6, sub.MaxDeliver)
	assert.Equal(t, []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}, sub.Backoff)
	assert.Equal(t, 256, sub.MaxInFlight)
}

// TestHandler_SubscriptionComesFromTheGivenTopology: the consumer is looked
// up in the topology the process installed, not in events.Default() -- the
// carried "consumer lookup is inconsistent" item.
func TestHandler_SubscriptionComesFromTheGivenTopology(t *testing.T) {
	topo := events.Default()
	for i := range topo.Consumers {
		if topo.Consumers[i].Name == events.ConsumerCatalogRSSMatcher {
			topo.Consumers[i].AckWait = 42 * time.Second
		}
	}
	sub := rssmatcher.NewHandler(rssmatcher.Deps{Topology: &topo}).Subscription()
	assert.Equal(t, 42*time.Second, sub.AckWait)
}
