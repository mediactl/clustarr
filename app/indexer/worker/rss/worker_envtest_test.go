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

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/app/indexer/health"
	"github.com/mediactl/clustarr/app/indexer/status/statuspatch"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// driveToSteadyState populates the Indexer the way a live system would --
// half by the Indexer controller's index manager, half by its index-worker
// manager -- and the agent's own half, the clustarr-indexer-health record on
// bus: a search that indexed 4235 releases and a completed RSS poll at t0
// that indexed 7 (ADR-0019 A6.3: the poll reports in the record and writes
// no Indexer status).
//
// Every test below that asserts a field SURVIVED a write depends on this
// running first. A test that acts on a blank object or an absent record
// cannot observe a lost field at all, because there was nothing to lose --
// which is how this defect class survived three reviews in Phase C.
func driveToSteadyState(t *testing.T, ctx context.Context, c client.Client, bus events.Bus, ns, name string) {
	t.Helper()
	idx := getIndexer(t, ctx, c, ns, name)

	require.NoError(t, statuspatch.Patch(ctx, c, k8s.ManagerIndex, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithProtocol(commonv1.ProtocolTorrent).
				WithPrivacy("private").
				WithSessionSecretRef(name + "-session").
				// The reconciler's projection of the rings since 2026-10-01.
				WithQueriesInWindow(11).
				WithGrabsInWindow(3)
		}))

	idx = getIndexer(t, ctx, c, ns, name)
	require.NoError(t, statuspatch.Patch(ctx, c, k8s.ManagerIndexWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithLastRssAt(metav1.NewTime(t0)).
				WithLastRssNewCount(7).
				WithIndexedReleases(4242)
		}))

	st := getStatus(t, ctx, c, ns, name)
	require.Equal(t, int32(11), st.QueriesInWindow, "setup: the steady state did not take")
	require.Equal(t, int64(4242), st.IndexedReleases, "setup: the steady state did not take")
	require.Equal(t, commonv1.ProtocolTorrent, st.Protocol, "setup: the steady state did not take")

	hw := healthOn(bus)
	idx = getIndexer(t, ctx, c, ns, name)
	require.NoError(t, hw.Outcome(ctx, refOf(idx), true, "", "", 4235))
	require.NoError(t, hw.RSS(ctx, refOf(idx), t0, 7, true, "", ""))
	rec := mustHealthRecord(t, bus, idx.UID)
	require.Equal(t, int64(4242), rec.IndexedReleases, "setup: the record's steady state did not take")
	require.Equal(t, int32(7), rec.LastRssNewCount, "setup: the record's steady state did not take")
}

// refOf is the record's reference to idx, as the worker builds it.
func refOf(idx *indexv1alpha1.Indexer) schema.ItemRef {
	return schema.ItemRef{
		Kind: health.IndexerKind,
		Ref:  schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
	}
}

// healthAt is healthOn with its clock pinned to the poll's, so a recorded
// failure's time is the poll's.
func healthAt(bus events.Bus, at time.Time) *health.Writer {
	hw := healthOn(bus)
	hw.Now = func() time.Time { return at }
	return hw
}

func newEnvtestWorker(t *testing.T, c client.Client, bus events.Bus, at time.Time, for_ func(string) rss.Searcher) *rss.Worker {
	t.Helper()
	return rss.NewWorker(rss.Deps{
		Client: c,
		Health: healthAt(bus, at),
		Bus:    bus,
		Index:  &fakeStore{},
		SearcherFor: func(_ context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return for_(idx.Name), nil
		},
		Clock: func() time.Time { return at },
	})
}

// requireIndexerUntouched holds the whole object: the poll writes no
// Indexer status at all (ADR-0019 A6.3), so neither its status nor its
// resourceVersion moves.
func requireIndexerUntouched(t *testing.T, before, after *indexv1alpha1.Indexer) {
	t.Helper()
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "the poll wrote the Indexer")
	require.Equal(t, before.Status, after.Status, "the poll changed Indexer status")
}

// TestStatusApplyDoesNotReleaseWhatItDidNotChange: a poll that finds
// nothing new changes the record's lastRssAt and lastRssNewCount and nothing
// else -- every other field of the record, and the whole Indexer, must still
// be there afterwards. (Before ADR-0019 A6.3 the poll applied Indexer status
// under a shared field manager, and the hazard was a server-side-apply
// release; the record's read-modify-write must keep the same promise.)
func TestStatusApplyDoesNotReleaseWhatItDidNotChange(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	before := getIndexer(t, ctx, c, ns, "idx")

	at := tNow
	// A poll that fetched nothing: no rows offered, nothing inserted.
	w := newEnvtestWorker(t, c, bus, at, func(string) rss.Searcher { return &fakeSearcher{} })
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	rec := mustHealthRecord(t, bus, before.UID)
	require.Equal(t, int64(4242), rec.IndexedReleases, "lost by a write that did not change it")
	require.Zero(t, rec.Failures)

	// What the poll genuinely changed.
	require.NotNil(t, rec.LastRssAt)
	require.Equal(t, at, rec.LastRssAt.UTC())
	require.Zero(t, rec.LastRssNewCount)
	require.Equal(t, int64(3), rec.Successes)

	// And the Indexer is untouched: the controller writes status.
	requireIndexerUntouched(t, before, getIndexer(t, ctx, c, ns, "idx"))
}

// TestFailedPollKeepsTheRssFieldsItDidNotChange is the dangerous one. A full
// queue, a 429, an indexer timeout: all transient, all routine, and a report
// that wrote only the failure would wipe lastRssAt, lastRssNewCount and
// indexedReleases off a healthy record.
func TestFailedPollKeepsTheRssFieldsItDidNotChange(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	before := getIndexer(t, ctx, c, ns, "idx")

	at := tNow
	w := newEnvtestWorker(t, c, bus, at, func(string) rss.Searcher {
		return &fakeSearcher{err: errors.New("indexer returned 503")}
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")),
		"a failed poll is reported; the Indexer controller runs the ladder and schedules the next one")

	rec := mustHealthRecord(t, bus, before.UID)
	require.Equal(t, int64(1), rec.Failures, "the failure was recorded")
	require.Equal(t, "indexer returned 503", rec.LastFailure)
	require.Equal(t, schema.IndexerFailureError, rec.LastFailureClass)
	require.NotNil(t, rec.LastFailureAt)
	require.Equal(t, at, rec.LastFailureAt.UTC())

	// The fields the failure path did not touch must survive it.
	require.Equal(t, int64(4242), rec.IndexedReleases)
	require.Equal(t, int32(7), rec.LastRssNewCount)
	require.NotNil(t, rec.LastRssAt)
	require.Equal(t, t0, rec.LastRssAt.UTC(), "a 503 must not erase when we last polled successfully")
	require.Equal(t, int64(2), rec.Successes)

	requireIndexerUntouched(t, before, getIndexer(t, ctx, c, ns, "idx"))
}

// TestRecoveryClearsTheEscalationRatherThanCarryingItForward is the mirror
// image. Clearing the escalation is the Indexer controller's ladder since
// ADR-0019 A6.3 (TestTheLadderRunsInTheController); what it reads is that
// the record's latest outcome is a success, so a recovered poll must stamp
// lastSuccessAt past the failure -- without erasing the failure history the
// ladder measures the run from.
func TestRecoveryClearsTheEscalationRatherThanCarryingItForward(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	uid := getIndexer(t, ctx, c, ns, "idx").UID

	// One failure puts it on the ladder.
	failedAt := tNow
	failing := newEnvtestWorker(t, c, bus, failedAt, func(string) rss.Searcher {
		return &fakeSearcher{err: errors.New("indexer returned 503")}
	})
	require.NoError(t, failing.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	down := mustHealthRecord(t, bus, uid)
	require.Equal(t, int64(1), down.Failures)
	require.NotNil(t, down.LastFailureAt)

	// A later, successful poll, after the window has passed.
	recoveredAt := failedAt.Add(10 * time.Minute)
	ok := newEnvtestWorker(t, c, bus, recoveredAt, func(string) rss.Searcher {
		return &fakeSearcher{releases: pageOf(2)}
	})
	require.NoError(t, ok.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	up := mustHealthRecord(t, bus, uid)
	require.NotNil(t, up.LastSuccessAt)
	require.Equal(t, recoveredAt, up.LastSuccessAt.UTC(), "the recovery is not the latest outcome")
	require.True(t, up.LastSuccessAt.After(*up.LastFailureAt), "a recovered indexer that reads failed never polls again")
	require.Equal(t, int64(1), up.Failures, "the failure history was erased")
	require.Equal(t, recoveredAt, up.LastRssAt.UTC())
}

// TestOneFailingIndexerDoesNotStopTheOthers. Isolation is structural -- one
// message is one indexer -- but it has to be asserted, because the cheap
// implementation (list every Indexer and loop) looks reasonable and destroys
// it.
func TestOneFailingIndexerDoesNotStopTheOthers(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "broken")
	newIndexer(t, ctx, c, ns, "healthy")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "broken")
	driveToSteadyState(t, ctx, c, bus, ns, "healthy")

	store := &fakeStore{inserted: 3}
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Health: healthAt(bus, tNow),
		Bus:    bus,
		Index:  store,
		SearcherFor: func(_ context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			if idx.Name == "broken" {
				return &fakeSearcher{err: errors.New("503")}, nil
			}
			return &fakeSearcher{releases: pageOf(3)}, nil
		},
		Clock: func() time.Time { return tNow },
	})

	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "broken")), "a failed poll is reported")
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "healthy")),
		"one indexer's failure must not reach another's delivery")

	require.Equal(t, int64(1), mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "broken").UID).Failures)

	good := mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "healthy").UID)
	require.Zero(t, good.Failures, "one indexer's failure reached another's record")
	require.Equal(t, int32(3), good.LastRssNewCount)
	require.Equal(t, int64(4242+3), good.IndexedReleases)
}

// TestIndexedReleasesIsAReadModifyWriteOnTheLiveObject proves the running
// total advances across polls rather than being recomputed from one poll:
// each poll's report is a read-modify-write of the live record.
func TestIndexedReleasesIsAReadModifyWriteOnTheLiveObject(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")

	for i := range 3 {
		w := rss.NewWorker(rss.Deps{
			Client: c,
			Health: healthOn(bus),
			Bus:    bus,
			Index:  &fakeStore{inserted: 2},
			SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
				return &fakeSearcher{releases: pageOf(2)}, nil
			},
			Clock: func() time.Time { return tNow.Add(time.Duration(i+1) * time.Hour) },
		})
		require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))
	}
	require.Equal(t, int64(4242+6), mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "idx").UID).IndexedReleases)
}

// TestTheWorkerNeverWritesTheControllersFields checks the split from the
// managedFields side. Since ADR-0019 A6.3 the poll writes no Indexer status
// at all -- the Indexer controller incorporates the health record under both
// of its managers -- so a successful poll leaves every manager's field set,
// and the object, exactly as it found them.
func TestTheWorkerNeverWritesTheControllersFields(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	before := getIndexer(t, ctx, c, ns, "idx")

	w := newEnvtestWorker(t, c, bus, tNow, func(string) rss.Searcher {
		return &fakeSearcher{releases: pageOf(2)}
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	after := getIndexer(t, ctx, c, ns, "idx")
	requireIndexerUntouched(t, before, after)
	owned := func(idx *indexv1alpha1.Indexer) map[string]string {
		out := map[string]string{}
		for _, mf := range idx.ManagedFields {
			if mf.Subresource == "status" && mf.FieldsV1 != nil {
				out[mf.Manager] = mf.FieldsV1.GetRawString()
			}
		}
		return out
	}
	require.Equal(t, owned(before), owned(after), "the poll claimed or released a status field")
}

// TestStoreFailureDoesNotWriteAPartialStatus: the index write is the step
// most likely to fail transiently (a full disk, a locked file), and it
// happens BEFORE the report. Failing there must leave the record -- and the
// Indexer -- exactly as they were rather than half-updated.
func TestStoreFailureDoesNotWriteAPartialStatus(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	before := getIndexer(t, ctx, c, ns, "idx")
	recBefore := mustHealthRecord(t, bus, before.UID)

	w := rss.NewWorker(rss.Deps{
		Client: c,
		Health: healthOn(bus),
		Bus:    bus,
		Index:  &fakeStore{err: errors.New("database is locked")},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: pageOf(2)}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.Error(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	rec := mustHealthRecord(t, bus, before.UID)
	require.Equal(t, recBefore, rec, "a poll whose index write failed reported a partial outcome")
	require.Equal(t, int64(4242), rec.IndexedReleases)
	require.Equal(t, int32(7), rec.LastRssNewCount)
	require.Equal(t, t0, rec.LastRssAt.UTC())
	requireIndexerUntouched(t, before, getIndexer(t, ctx, c, ns, "idx"))
}

// Compile-time proof that the fakes really do stand in for the production
// seams, rather than for a signature that has drifted.
var (
	_ relindex.Store = (*fakeStore)(nil)
	_ rss.Searcher   = (*fakeSearcher)(nil)
)

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
	bus, _ := newJetStreamBusAndConn(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")

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
		Health: healthOn(bus),
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
// beside it -- and because indexAndPublish returned before both the status
// write and ScheduleNext, the indexer kept reading healthy while its RSS
// chain ended for good: nothing published, no escalation recorded, and after
// MaxDeliver the delivery dead-lettered with no pending schedule left to
// re-seed it. (Since ADR-0019 A6.3 the poll's report is the health record,
// from which the Indexer controller schedules the next poll.)
//
// The store here is a REAL sqlite index, not the fake: the fake accepts
// anything, so it could not have caught this.
func TestOneUnindexableRowDoesNotLoseThePage(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus, _ := newJetStreamBusAndConn(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

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
		Health: healthOn(bus),
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

	// The poll was reported, so the chain continues: this is the half that
	// made the original defect permanent rather than merely lossy.
	rec := mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "idx").UID)
	require.Equal(t, int32(2), rec.LastRssNewCount, "new means new to the INDEX, and the junk rows never got there")
	require.Equal(t, int64(4242+2), rec.IndexedReleases)
	require.Equal(t, tNow, rec.LastRssAt.UTC())
	require.Zero(t, rec.Failures, "a junk row is not the indexer failing")
}

// TestAPageOfNothingButJunkStillKeepsTheChainAlive is the degenerate case:
// every row is unindexable, so there is nothing to insert and nothing to
// publish. The poll must still report itself: the Indexer controller
// schedules the next poll from the record (ADR-0019 A6.3, P91;
// TestTheControllerSchedulesTheNextPollAfterAnOutcome).
func TestAPageOfNothingButJunkStillKeepsTheChainAlive(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus, _ := newJetStreamBusAndConn(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	w := rss.NewWorker(rss.Deps{
		Client: c,
		Health: healthOn(bus),
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

	rec := mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "idx").UID)
	require.Zero(t, rec.LastRssNewCount)
	require.Equal(t, int64(4242), rec.IndexedReleases)
	require.Equal(t, tNow, rec.LastRssAt.UTC(),
		"the poll happened, even though it yielded nothing -- the chain must not end on a page of junk")
	require.Zero(t, rec.Failures)
}

// TestAConcurrentWorkerWriteSurvivesALongPoll is I3: a report seeded from a
// read taken before a minutes-long poll would roll back whatever else wrote
// the same record meanwhile -- here the search fan-out recording 100 newly
// indexed releases through its own writer.
//
// This is a lost update: both writes carry the field, the second just
// carries a stale value. The record's write is a read-modify-write after the
// poll, so the poll's increment lands on the fan-out's.
func TestAConcurrentWorkerWriteSurvivesALongPoll(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx") // indexedReleases = 4242
	idx := getIndexer(t, ctx, c, ns, "idx")

	// The search fan-out lands mid-poll, through its own writer.
	fanOut := healthOn(bus)
	searched := false
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Health: healthOn(bus),
		Bus:    bus,
		Index:  &fakeStore{inserted: 1},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
				if !searched {
					searched = true
					require.NoError(t, fanOut.Outcome(ctx, refOf(idx), true, "", "", 100))
				}
				return pageOf(1), nil
			}}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))

	rec := mustHealthRecord(t, bus, idx.UID)
	require.Equal(t, int64(4242+100+1), rec.IndexedReleases,
		"the poll seeded its report from a pre-poll snapshot and rolled the search's increment back")
	require.Equal(t, int32(1), rec.LastRssNewCount, "and the poll still recorded its own result")
}

// racingKV is the health bucket with a second writer in the one window a
// re-read cannot close: between the read the report is seeded from and the
// write itself. On its first Get it lets the read return and then reports
// another search's outcome -- 100 newly indexed releases -- through a
// second writer.
type racingKV struct {
	events.KV
	t     *testing.T
	other *health.Writer
	ref   schema.ItemRef
	raced bool
}

func (r *racingKV) Get(ctx context.Context, key string) (events.Entry, error) {
	e, err := r.KV.Get(ctx, key)
	if err != nil {
		return e, err
	}
	if !r.raced {
		r.raced = true
		require.NoError(r.t, r.other.Outcome(ctx, r.ref, true, "", "", 100))
	}
	return e, nil
}

// The window a re-read leaves open: a search outcome landing between the
// poll's read and its write. The record's write is a compare-and-swap, so
// the lost race sends it back to a fresh read, and the increment it adds is
// computed from that.
func TestAWriteBetweenThePollsReadAndApplyIsNotRolledBack(t *testing.T) {
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "idx")
	bus := newTestBus(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")
	idx := getIndexer(t, ctx, c, ns, "idx")

	racing := &racingKV{
		KV: bus.KV(events.BucketIndexerHealth), t: t, other: healthOn(bus), ref: refOf(idx),
	}
	w := rss.NewWorker(rss.Deps{
		Client: c,
		Health: &health.Writer{Records: records.NewWriter(racing, agentrecords.IndexerHealth(), "index-test", "test")},
		Bus:    bus,
		Index:  &fakeStore{inserted: 2},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return &fakeSearcher{releases: pageOf(2)}, nil
		},
		Clock: func() time.Time { return tNow },
	})
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "idx")))
	require.True(t, racing.raced, "the second writer never ran")

	rec := mustHealthRecord(t, bus, idx.UID)
	require.Equal(t, int64(4242+100+2), rec.IndexedReleases, "an increment was lost between the read and the write")
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
	bus, _ := newJetStreamBusAndConn(t)
	driveToSteadyState(t, ctx, c, bus, ns, "idx")

	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

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
		Client: c, Health: healthOn(bus), Bus: bus, Index: store,
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

	rec := mustHealthRecord(t, bus, getIndexer(t, ctx, c, ns, "idx").UID)
	require.Equal(t, int32(1), rec.LastRssNewCount, "new means new to the INDEX")
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
