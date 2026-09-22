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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/relindex"
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
// indexarr/status.WorkerFields seeds the apply from the LIVE status, so
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
