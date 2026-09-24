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

package rss_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// driveToSteadyState populates the object the way a live system would: half
// by the Indexer reconciler's manager, half by the worker's.
//
// Every test below that asserts a field SURVIVED an apply depends on this
// running first. A test that creates a blank object, triggers the path and
// asserts cannot observe a server-side-apply release at all, because there
// was nothing on the object to release -- which is how this defect class
// survived three reviews in Phase C.
func driveToSteadyState(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	idx := getIndexer(t, ctx, c, ns, name)

	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarr, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithProtocol(commonv1.ProtocolTorrent).
				WithPrivacy("private").
				WithSessionSecretRef(name + "-session")
		}))

	idx = getIndexer(t, ctx, c, ns, name)
	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithLastRssAt(metav1.NewTime(t0)).
				WithLastRssNewCount(7).
				WithIndexedReleases(4242).
				WithQueriesInWindow(11).
				WithGrabsInWindow(3)
		}))

	st := getStatus(t, ctx, c, ns, name)
	require.Equal(t, int32(11), st.QueriesInWindow, "setup: the steady state did not take")
	require.Equal(t, int64(4242), st.IndexedReleases, "setup: the steady state did not take")
	require.Equal(t, commonv1.ProtocolTorrent, st.Protocol, "setup: the steady state did not take")
}

func newEnvtestWorker(t *testing.T, c client.Client, at time.Time, for_ func(string) rss.Searcher) *rss.Worker {
	t.Helper()
	return rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    newTestBus(t),
		Index:  &fakeStore{},
		SearcherFor: func(_ context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return for_(idx.Name), nil
		},
		Clock: func() time.Time { return at },
	})
}

// TestStatusApplyDoesNotReleaseWhatItDidNotChange is the test this task's
// status handling exists for.
//
// Server-side apply REPLACES a field manager's ownership set on every apply
// rather than merging into it, so a field the manager sent before and omits
// now is released, and reads as zero on the object. A poll that finds nothing
// new changes lastRssAt and lastRssNewCount and nothing else -- so every
// other indexarr-worker field, and every indexarr field, must still be there
// afterwards.
func TestStatusApplyDoesNotReleaseWhatItDidNotChange(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	at := tNow
	// A poll that fetched nothing: no rows offered, nothing inserted.
	w := newEnvtestWorker(t, c, at, func(string) rss.Searcher { return &fakeSearcher{} })
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	after := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(11), after.QueriesInWindow, "released by an apply that omitted it")
	require.Equal(t, int32(3), after.GrabsInWindow)
	require.Equal(t, int64(4242), after.IndexedReleases)

	// What the poll genuinely changed.
	require.NotNil(t, after.LastRssAt)
	require.Equal(t, at, after.LastRssAt.UTC())
	require.Zero(t, after.LastRssNewCount)

	// And the OTHER manager's fields must be untouched: that is the whole
	// point of splitting Indexer.status by field manager.
	require.Equal(t, int64(1), after.ObservedGeneration)
	require.Equal(t, commonv1.ProtocolTorrent, after.Protocol)
	require.Equal(t, "idx-session", after.SessionSecretRef)
	require.Equal(t, "private", after.Privacy)
}

// TestFailedPollKeepsTheRssFieldsItDidNotChange is the dangerous one. A full
// queue, a 429, an indexer timeout: all transient, all routine, and an early
// return that applies only an escalation wipes lastRssAt, lastRssNewCount and
// indexedReleases off a healthy object.
func TestFailedPollKeepsTheRssFieldsItDidNotChange(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	at := tNow
	w := newEnvtestWorker(t, c, at, func(string) rss.Searcher {
		return &fakeSearcher{err: errors.New("indexer returned 503")}
	})
	require.Error(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")), "a failed poll is retried")

	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(1), st.EscalationLevel, "the failure was recorded")
	require.Equal(t, "indexer returned 503", st.LastFailure)
	require.NotNil(t, st.InitialFailureAt)
	require.Equal(t, at, st.InitialFailureAt.UTC())

	// The fields the failure path did not touch must survive it.
	require.Equal(t, int64(4242), st.IndexedReleases)
	require.Equal(t, int32(7), st.LastRssNewCount)
	require.NotNil(t, st.LastRssAt)
	require.Equal(t, t0, st.LastRssAt.UTC(), "a 503 must not erase when we last polled successfully")
	require.Equal(t, int32(11), st.QueriesInWindow)
	require.Equal(t, int32(3), st.GrabsInWindow)

	// The other manager's set is still whole too.
	require.Equal(t, commonv1.ProtocolTorrent, st.Protocol)
	require.Equal(t, "idx-session", st.SessionSecretRef)
}

// TestRecoveryClearsTheEscalationRatherThanCarryingItForward is the mirror
// image, and it is why the escalation's pointer fields are ASSIGNED onto the
// apply configuration instead of set through the generated With* helpers.
//
// app/indexer/status.WorkerFields seeds the apply from the LIVE status, so
// disabledUntil, lastFailureAt and initialFailureAt all arrive pre-set. A
// writer that only ever added to that seed could never clear them, and a
// recovered indexer would stay disabled until something else rewrote the
// object.
func TestRecoveryClearsTheEscalationRatherThanCarryingItForward(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	// One failure puts it on the ladder.
	failedAt := tNow
	failing := newEnvtestWorker(t, c, failedAt, func(string) rss.Searcher {
		return &fakeSearcher{err: errors.New("indexer returned 503")}
	})
	require.Error(t, failing.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	down := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(1), down.EscalationLevel)
	require.NotNil(t, down.DisabledUntil, "setup: level 1 disables for a minute")
	require.NotNil(t, down.LastFailureAt)
	require.NotNil(t, down.InitialFailureAt)

	// A later, successful poll, after the window has passed.
	recoveredAt := failedAt.Add(10 * time.Minute)
	ok := newEnvtestWorker(t, c, recoveredAt, func(string) rss.Searcher {
		return &fakeSearcher{releases: pageOf(2)}
	})
	require.NoError(t, ok.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	up := getStatus(t, ctx, c, ns, "idx")
	require.Zero(t, up.EscalationLevel)
	require.Nil(t, up.DisabledUntil, "a recovered indexer that stays disabled never polls again")
	require.Nil(t, up.LastFailureAt)
	require.Nil(t, up.InitialFailureAt)
	require.Empty(t, up.LastFailure)

	// ... and the recovery apply still carried everything else.
	require.Equal(t, int32(11), up.QueriesInWindow)
	require.Equal(t, int32(3), up.GrabsInWindow)
	require.Equal(t, commonv1.ProtocolTorrent, up.Protocol)
}

// TestOneFailingIndexerDoesNotStopTheOthers. Isolation is structural -- one
// message is one indexer -- but it has to be asserted, because the cheap
// implementation (list every Indexer and loop) looks reasonable and destroys
// it.
func TestOneFailingIndexerDoesNotStopTheOthers(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "broken")
	newIndexer(t, ctx, c, ns, "healthy")
	driveToSteadyState(t, ctx, c, ns, "broken")
	driveToSteadyState(t, ctx, c, ns, "healthy")

	store := &fakeStore{inserted: 3}
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    newTestBus(t),
		Index:  store,
		SearcherFor: func(_ context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			if idx.Name == "broken" {
				return &fakeSearcher{err: errors.New("503")}, nil
			}
			return &fakeSearcher{releases: pageOf(3)}, nil
		},
		Clock: func() time.Time { return tNow },
	})

	require.Error(t, w.Handle(ctx, rssTaskMessage(t, ns, "broken")))
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "healthy")),
		"one indexer's failure must not reach another's delivery")

	require.Equal(t, int32(1), getStatus(t, ctx, c, ns, "broken").EscalationLevel)

	good := getStatus(t, ctx, c, ns, "healthy")
	require.Zero(t, good.EscalationLevel)
	require.Equal(t, int32(3), good.LastRssNewCount)
	require.Equal(t, int64(4242+3), good.IndexedReleases)
}

// TestIndexedReleasesIsAReadModifyWriteOnTheLiveObject proves the running
// total advances across polls rather than being recomputed from one poll.
func TestIndexedReleasesIsAReadModifyWriteOnTheLiveObject(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	for i := range 3 {
		w := rss.NewWorker(rss.Deps{
			Client: c,
			Bus:    newTestBus(t),
			Index:  &fakeStore{inserted: 2},
			SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
				return &fakeSearcher{releases: pageOf(2)}, nil
			},
			Clock: func() time.Time { return tNow.Add(time.Duration(i+1) * time.Hour) },
		})
		require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))
	}
	require.Equal(t, int64(4242+6), getStatus(t, ctx, c, ns, "idx").IndexedReleases)
}

// TestTheWorkerNeverWritesTheControllersFields checks the split from the
// managedFields side: whatever the worker touched, it must not have claimed
// ownership of anything in the reconciler's set.
func TestTheWorkerNeverWritesTheControllersFields(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	w := newEnvtestWorker(t, c, tNow, func(string) rss.Searcher {
		return &fakeSearcher{releases: pageOf(2)}
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	idx := getIndexer(t, ctx, c, ns, "idx")
	var owned string
	for _, mf := range idx.ManagedFields {
		if mf.Manager == string(k8s.ManagerIndexarrWorker) && mf.Subresource == "status" {
			owned = mf.FieldsV1.GetRawString()
		}
	}
	require.NotEmpty(t, owned, "the worker must own a status field set")
	for _, forbidden := range []string{"observedGeneration", "protocol", "privacy", "sessionSecretRef", "caps", "conditions"} {
		require.NotContains(t, owned, `"f:`+forbidden+`"`,
			"the worker claimed %q, which belongs to the indexarr manager", forbidden)
	}
	for _, required := range []string{"lastRssAt", "lastRssNewCount", "indexedReleases", "queriesInWindow", "grabsInWindow", "escalationLevel"} {
		require.Contains(t, owned, `"f:`+required+`"`,
			"the worker stopped declaring %q, so the next apply releases it", required)
	}
}

// TestStoreFailureDoesNotWriteAPartialStatus: the index write is the step
// most likely to fail transiently (a full disk, a locked file), and it
// happens BEFORE the status apply. Failing there must leave the object
// exactly as it was rather than half-updated.
func TestStoreFailureDoesNotWriteAPartialStatus(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    newTestBus(t),
		Index:  &fakeStore{err: errors.New("database is locked")},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: pageOf(2)}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.Error(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int64(4242), st.IndexedReleases)
	require.Equal(t, int32(7), st.LastRssNewCount)
	require.Equal(t, t0, st.LastRssAt.UTC())
	require.Equal(t, int32(11), st.QueriesInWindow)
}

// Compile-time proof that the fakes really do stand in for the production
// seams, rather than for a signature that has drifted.
var (
	_ relindex.Store = (*fakeStore)(nil)
	_ rss.Searcher   = (*fakeSearcher)(nil)
)

// TestATypedClientIndexerIsNotPolledInAHotLoop is the end-to-end form of the
// rssInterval floor, and it is here rather than beside the unit test because
// the hazard is a property of the WIRE, not of the Go value.
//
// The Indexer below is created through the typed client with no
// spec.rssInterval, exactly as every envtest and every future in-cluster
// creator does. metav1.Duration is a struct and `omitempty` cannot drop it,
// so client-go sends "rssInterval":"0s" and the apiserver never applies the
// CRD's 15m default. The object the worker then reads back says zero.
//
// Unfloored, the next poll is scheduled at `now`, which the broker can
// redeliver immediately -- one indexer polled as fast as JetStream will hand
// the task back. A test that only asserted "a next task was scheduled" would
// pass on exactly that.
func TestATypedClientIndexerIsNotPolledInAHotLoop(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")

	// The premise, stated rather than assumed: the apiserver did NOT default
	// it. If this ever starts failing, the floor may no longer be needed --
	// but find out why before removing it.
	require.Zero(t, getIndexer(t, ctx, c, ns, "idx").Spec.RssInterval.Duration,
		"premise changed: a typed create now gets the CRD default")

	bus, nc := newJetStreamBusAndConn(t)
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  &fakeStore{inserted: 1},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: pageOf(1)}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	uid := string(getIndexer(t, ctx, c, ns, "idx").UID)
	require.Equal(t, "@at "+tNow.Add(15*time.Minute).Format(time.RFC3339),
		pendingSchedule(t, nc, uid).schedule,
		"an unfloored zero interval schedules the next poll at now, and the broker redelivers it immediately")
}

// TestHandlePublishesEveryPolledReleaseToTheFirehose closes the gap this whole
// task exists to close: a producer that does not produce.
//
// PublishReleases is well covered on its own, but nothing connected the poll
// to it -- deleting the call from indexAndPublish left the entire suite green.
// This drives Handle end to end and consumes through the SHIPPED matcher
// subscription, which also pins the envelope key to the Indexer object's own
// metadata.namespace/metadata.name rather than to PublishReleases' arguments.
func TestHandlePublishesEveryPolledReleaseToTheFirehose(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	bus, _ := newJetStreamBusAndConn(t)

	var mu sync.Mutex
	seen := map[string]*events.Envelope{}
	stop, err := bus.Subscribe(ctx, (&rssmatcher.Handler{}).Subscription(),
		func(_ context.Context, m events.Message) error {
			var rel schema.Release
			if decErr := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &rel); decErr != nil {
				return decErr
			}
			mu.Lock()
			defer mu.Unlock()
			seen[rel.Info.GUID] = m.Envelope().Clone()
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(stop)

	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  &fakeStore{inserted: 3},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: pageOf(3)}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 3
	}, 20*time.Second, 20*time.Millisecond,
		"a poll that fetches releases and publishes none is the one failure this task exists to prevent")

	mu.Lock()
	defer mu.Unlock()
	for guid, env := range seen {
		gotNS, gotIdx, ok := strings.Cut(env.Key, "/")
		require.True(t, ok, "key %q has no slash: the matcher dead-letters it without a retry", env.Key)
		require.Equal(t, ns, gotNS, "the namespace on the wire is the Indexer object's own")
		require.Equal(t, "idx", gotIdx)
		require.Equal(t, events.MsgIDForRelease("idx", guid), env.ID)
		require.Equal(t, tNow, env.Time.UTC(), "the publisher stamps FetchedAt and the envelope from one clock")
	}
}

// TestOneUnindexableRowDoesNotLoseThePage is the regression for the batch
// kill.
//
// relindex.Upsert validates EVERY row before it opens its transaction and
// fails the whole batch, so one junk row used to lose every good release
// beside it -- and because indexAndPublish returns before both the status
// write and ScheduleNext, the indexer kept reading healthy while its RSS
// chain ended for good: nothing published, no escalation recorded, and after
// MaxDeliver the delivery dead-lettered with no pending schedule left to
// re-seed it.
//
// The store here is a REAL sqlite index, not the fake: the fake accepts
// anything, so it could not have caught this.
func TestOneUnindexableRowDoesNotLoseThePage(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	bus, _ := newJetStreamBusAndConn(t)
	var mu sync.Mutex
	var published []string
	stop, err := bus.Subscribe(ctx, (&rssmatcher.Handler{}).Subscription(),
		func(_ context.Context, m events.Message) error {
			var rel schema.Release
			if decErr := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &rel); decErr != nil {
				return decErr
			}
			mu.Lock()
			defer mu.Unlock()
			published = append(published, rel.Info.GUID)
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(stop)

	// Two good rows and three an indexer can genuinely emit: a symbol-only
	// title, a title of CJK punctuation alone, and an item with no guid at
	// all.
	//
	// The CJK fixture is brackets, not words, on purpose. release.TitleNorm
	// keeps letters in every script, so "日本語のタイトル" is a perfectly
	// storable row now, and a fixture that used it would prove nothing while
	// looking like it proved everything.
	feed := append(pageOf(2),
		torznab.Release{Title: "★★★", GUID: "junk-symbols"},
		torznab.Release{Title: "「」【】", GUID: "junk-cjk-punctuation"},
		torznab.Release{Title: "Another.Movie.2011.1080p-GRP", GUID: ""},
	)
	// State the premise rather than trusting it: each junk fixture must
	// genuinely be one the index refuses, or the test proves nothing while
	// looking like it proves everything.
	for _, junk := range feed[2:] {
		require.True(t, release.TitleNorm(junk.Title) == "" || junk.GUID == "",
			"fixture %q is perfectly storable, so it exercises nothing", junk.Title)
	}

	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  store,
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: feed}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")),
		"one malformed feed row must not fail the delivery")

	// The two good rows reached the index.
	stored, err := store.Search(ctx, relindex.Query{})
	require.NoError(t, err)
	require.Len(t, stored, 2, "the good rows were lost with the junk one")

	// ... and the firehose.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(published) == 2
	}, 20*time.Second, 20*time.Millisecond, "the good rows were never published")
	mu.Lock()
	require.ElementsMatch(t, []string{"guid-0", "guid-1"}, published)
	mu.Unlock()

	// The status was written and the chain continues: this is the half that
	// made the original defect permanent rather than merely lossy.
	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(2), st.LastRssNewCount, "new means new to the INDEX, and the junk rows never got there")
	require.Equal(t, int64(4242+2), st.IndexedReleases)
	require.Equal(t, tNow, st.LastRssAt.UTC())
	require.Zero(t, st.EscalationLevel, "a junk row is not the indexer failing")
}

// TestAPageOfNothingButJunkStillKeepsTheChainAlive is the degenerate case:
// every row is unindexable, so there is nothing to insert and nothing to
// publish. The poll must still record itself and schedule the next one.
func TestAPageOfNothingButJunkStillKeepsTheChainAlive(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	bus, nc := newJetStreamBusAndConn(t)
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  store,
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: []torznab.Release{
				{Title: "★★★", GUID: "a"}, {Title: "???", GUID: "b"},
			}}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	st := getStatus(t, ctx, c, ns, "idx")
	require.Zero(t, st.LastRssNewCount)
	require.Equal(t, int64(4242), st.IndexedReleases)
	require.Equal(t, tNow, st.LastRssAt.UTC(), "the poll happened, even though it yielded nothing")

	uid := string(getIndexer(t, ctx, c, ns, "idx").UID)
	require.Equal(t, "@at "+tNow.Add(15*time.Minute).Format(time.RFC3339),
		pendingSchedule(t, nc, uid).schedule, "the chain must not end on a page of junk")
}

// TestAConcurrentWorkerWriteSurvivesALongPoll is I3: the status apply seeds
// from the object it was handed, so a snapshot taken before a minutes-long
// poll would roll back whatever else wrote under the SAME field manager
// meanwhile. The search fan-out shares k8s.ManagerIndexarrWorker and owns
// queriesInWindow and grabsInWindow.
//
// This is a lost update, not a server-side-apply release: both applies
// declare the field, the second just declares a stale value. No "manager X
// released field Y" test can observe it, which is why it needs its own.
func TestAConcurrentWorkerWriteSurvivesALongPoll(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx") // queriesInWindow = 11

	// The search fan-out lands mid-poll, under the same manager.
	searched := false
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    newTestBus(t),
		Index:  &fakeStore{inserted: 1},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
				if !searched {
					searched = true
					live := getIndexer(t, ctx, c, ns, "idx")
					require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, live,
						func(ac *indexac.IndexerStatusApplyConfiguration) {
							ac.WithQueriesInWindow(12).WithGrabsInWindow(4)
						}))
				}
				return pageOf(1), nil
			}}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(12), st.QueriesInWindow,
		"the poll seeded its apply from a pre-poll snapshot and rolled the search's count back")
	require.Equal(t, int32(4), st.GrabsInWindow)
	require.Equal(t, int32(1), st.LastRssNewCount, "and the poll still recorded its own result")
}

// counterValue reads a single-series counter without pulling in
// prometheus/client_golang/prometheus/testutil, which needs a module that is
// in go.sum but not declared in go.mod -- and go.mod is not this task's to
// touch. Same approach as app/catalog/metadata's own metric assertions.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// TestAnUnstorableTitleWithIDsStillReachesTheMatcher is the one case where
// "the index refuses it" and "the matcher cannot use it" come apart.
//
// rssmatcher dispatches movies and series on Info.IDs["tmdb"]/["tvdb"]
// BEFORE it looks at a title, and ProjectRelease carries the indexer's own
// id attrs onto Info.IDs even when the parse fails. So a release whose title
// normalises to nothing -- Cyrillic, CJK, punctuation -- but which carries an
// id is a perfectly matchable release that merely cannot be stored. Dropping
// it from the firehose as a side effect of a filter written for the index
// costs a real match.
//
// The empty-GUID case is the opposite and stays dropped from both: every
// such row hashes to the same Nats-Msg-Id, so the 2h dedup window would
// collapse them all into one message.
func TestAnUnstorableTitleWithIDsStillReachesTheMatcher(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	bus, _ := newJetStreamBusAndConn(t)
	var mu sync.Mutex
	published := map[string]schema.Release{}
	stop, err := bus.Subscribe(ctx, (&rssmatcher.Handler{}).Subscription(),
		func(_ context.Context, m events.Message) error {
			var rel schema.Release
			if decErr := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &rel); decErr != nil {
				return decErr
			}
			mu.Lock()
			defer mu.Unlock()
			published[rel.Info.GUID] = rel
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(stop)

	before := counterValue(t, metrics.IndexerReleasesDropped.WithLabelValues("idx"))

	feed := []torznab.Release{
		// Storable and matchable: the control.
		{Title: "Some.Movie.2000.1080p.BluRay.x264-GRP", GUID: "good"},
		// Unstorable title, but it carries an id the matcher keys on.
		{Title: "「」【】", GUID: "punct-with-id", IDs: map[string]string{commonv1.IDKeyTMDB: "603"}},
		// Unstorable title and nothing to match on: genuinely useless.
		{Title: "★★★", GUID: "symbols-no-id"},
		// No GUID: every one of these would hash to the same msg-id.
		{Title: "★★★", GUID: "", IDs: map[string]string{commonv1.IDKeyTMDB: "604"}},
	}
	for _, junk := range feed[1:] {
		require.Empty(t, release.TitleNorm(junk.Title),
			"fixture %q is storable, so it exercises nothing", junk.Title)
	}

	w := rss.NewWorker(rss.Deps{
		Client: c, Bus: bus, Index: store,
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: feed}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(published) == 2
	}, 20*time.Second, 20*time.Millisecond,
		"want the good release and the id-bearing one; got %v", publishedKeys(&mu, published))

	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, published, "good")

	idOnly, ok := published["punct-with-id"]
	require.True(t, ok, "a release the index cannot store can still match on its ids")
	require.Equal(t, "603", idOnly.Info.IDs[commonv1.IDKeyTMDB])
	require.Empty(t, idOnly.ParsedTitle, "it is published on its ids alone, not on a title")

	require.NotContains(t, published, "symbols-no-id", "nothing to store AND nothing to match on")
	require.NotContains(t, published, "",
		"an empty guid makes every such row share one Nats-Msg-Id; the dedup window would eat all but the first")

	// It is still refused by the index: publishing it does not store it.
	stored, err := store.Search(ctx, relindex.Query{})
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "good", stored[0].GUID)

	// ... and all three refusals are visible to an operator.
	require.Equal(t, float64(3), counterValue(t, metrics.IndexerReleasesDropped.WithLabelValues("idx"))-before,
		"a feed that is three-quarters garbage must not look identical to a quiet one")

	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(1), st.LastRssNewCount, "new means new to the INDEX")
}

func publishedKeys(mu *sync.Mutex, m map[string]schema.Release) []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
