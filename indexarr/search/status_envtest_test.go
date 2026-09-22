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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	searchworker "github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/indexarr/search"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// searchRequest is the shipped caller's own request, so these tests exercise
// the shape production actually sends.
func searchRequest(ns string) schema.SearchRequest {
	return searchworker.BuildSearchRequest(ns, commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205, ImdbID: "tt1375666", Year: 2010},
		schema.MaxSearchReleases, false, nil, nil)
}

// steadyState creates an Indexer and drives its status to a REAL steady
// state: a completed RSS poll (lastRssAt, lastRssNewCount), a release count
// and a grab count, all applied under the SAME indexarr-worker manager the
// search path uses, exactly as indexarr/worker/rss and indexarr/download
// would leave it.
//
// A test that skips this and acts on a blank object CANNOT observe a
// field-manager release, because there is nothing to release. There is also
// deliberately no second writer of these fields: two managers applying the
// same value CO-OWN it, so a release by one would leave the field standing
// and the test would pass whether or not the apply released it.
func steadyState(
	t *testing.T, ctx context.Context, c client.Client, ns, name string,
) *indexv1alpha1.Indexer {
	t.Helper()
	newNamespace(t, ctx, c, ns)

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://example.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolUsenet},
		},
	}
	require.NoError(t, c.Create(ctx, idx))

	// The Indexer reconciler's half, under ITS manager. It must survive
	// every apply this package makes: that disjointness is what ruling R6's
	// split exists for.
	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarr, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			*ac = *idxstatus.ControllerFields(indexv1alpha1.IndexerStatus{
				ObservedGeneration: 1,
				Protocol:           commonv1.ProtocolUsenet,
				Privacy:            "private",
				SessionSecretRef:   "nzbgeek-session",
				Caps: &indexv1alpha1.Caps{
					Modes:         map[string][]string{"movie": {"imdbid", "q", "tmdbid"}},
					Categories:    []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}},
					LimitsMax:     100,
					LimitsDefault: 50,
				},
			})
			ac.WithConditions(k8s.ConditionACs([]metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionTrue,
				Reason: "Probed", Message: "caps probed",
			}})...)
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))

	rssAt := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			*ac = *idxstatus.WorkerFields(indexv1alpha1.IndexerStatus{
				LastRssAt:       &rssAt,
				LastRssNewCount: 7,
				IndexedReleases: 1234,
				GrabsInWindow:   3,
				QueriesInWindow: 5,
			})
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.Equal(t, int32(5), idx.Status.QueriesInWindow, "the steady state did not take")
	require.NotNil(t, idx.Status.Caps, "the steady state did not take")
	return idx
}

func failingService(c client.Client, err error) *search.Service {
	return &search.Service{
		Client: c,
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return errClient{err: err}, nil
		},
	}
}

// pastTheStartupGrace moves the service's clock beyond
// idxstatus.StartupGrace. Inside that window a failure deliberately does NOT
// escalate -- a restart must not disable every indexer at once -- and the
// test process has only just started, so a real clock would exercise the
// non-escalating branch and prove nothing about the escalating one.
func pastTheStartupGrace() func() time.Time {
	return func() time.Time { return time.Now().Add(idxstatus.StartupGrace + time.Minute) }
}

// insertingStore reports every row it is given as newly inserted, which is
// what status.indexedReleases counts.
type insertingStore struct{ relindex.Store }

func (insertingStore) Upsert(_ context.Context, rels []relindex.Release) (int, error) {
	return len(rels), nil
}

type errClient struct{ err error }

func (e errClient) Search(context.Context, torznab.Query) ([]torznab.Release, error) {
	return nil, e.err
}

// Server-side apply REPLACES a manager's ownership set on every apply. The
// search fan-out and the RSS poll share k8s.ManagerIndexarrWorker, so an
// apply from here that declared less than the complete set would release
// whatever the poll had written -- silently, and reading as zero on the
// object.
func TestAFailingSearchDoesNotReleaseTheRSSFields(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-ssa", "nzbgeek")

	svc := failingService(c, errors.New("tracker exploded"))
	svc.Now = pastTheStartupGrace()
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))

	// The escalation landed ...
	require.Equal(t, int32(1), got.Status.EscalationLevel)
	require.NotNil(t, got.Status.DisabledUntil)
	require.NotNil(t, got.Status.LastFailureAt)
	require.NotNil(t, got.Status.InitialFailureAt)
	require.Contains(t, got.Status.LastFailure, "tracker exploded")

	// ... and NOTHING else this manager owns was released.
	require.NotNil(t, got.Status.LastRssAt, "lastRssAt was released by a partial apply")
	require.Equal(t, int32(7), got.Status.LastRssNewCount, "released by a partial apply")
	require.Equal(t, int64(1234), got.Status.IndexedReleases, "released by a partial apply")
	require.Equal(t, int32(3), got.Status.GrabsInWindow, "released by a partial apply")
	require.Equal(t, int32(5), got.Status.QueriesInWindow, "released by a partial apply")
}

// The mirror: the reconciler's own fields, under the OTHER manager, are
// untouched by a search. Two managers, disjoint sets, no re-assertion --
// which is what ruling R6's split was added for.
func TestASearchDoesNotTouchTheReconcilersFields(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-split", "nzbgeek")

	svc := failingService(c, errors.New("tracker exploded"))
	_ = svc.Search(ctx, searchRequest(idx.Namespace))

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int64(1), got.Status.ObservedGeneration)
	require.Equal(t, commonv1.ProtocolUsenet, got.Status.Protocol)
	require.Equal(t, "private", got.Status.Privacy)
	require.Equal(t, "nzbgeek-session", got.Status.SessionSecretRef)
	require.Len(t, got.Status.Conditions, 1)

	// Every leaf of caps, not just the parent: server-side apply tracks
	// ownership per leaf inside a struct, so an assertion that caps is
	// non-nil cannot see four of its five fields being released.
	require.NotNil(t, got.Status.Caps)
	require.Equal(t, map[string][]string{"movie": {"imdbid", "q", "tmdbid"}}, got.Status.Caps.Modes)
	require.Equal(t, []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}}, got.Status.Caps.Categories)
	require.Equal(t, int32(100), got.Status.Caps.LimitsMax)
	require.Equal(t, int32(50), got.Status.Caps.LimitsDefault)
	require.False(t, got.Status.Caps.SupportsRawSearch)
}

// A recovered indexer must have its disable CLEARED, and a With* helper
// cannot express that: WorkerFields seeds disabledUntil from the LIVE status
// and only omits it when nil, so a hand-rolled apply would carry the stale
// disable forward for ever. Nothing else clears it, nothing logs it, and the
// indexer would never be queried again.
func TestASuccessfulSearchClearsTheEscalation(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-recover", "nzbgeek")

	// Drive it into backoff first, the way a run of failures would.
	until := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	failedAt := metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Second))
	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			st := idx.Status
			st.EscalationLevel = 1
			st.DisabledUntil = &until // already expired, so Healthy is true again
			st.InitialFailureAt = &failedAt
			st.LastFailureAt = &failedAt
			st.LastFailure = "tracker exploded"
			*ac = *idxstatus.WorkerFields(st)
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.NotNil(t, idx.Status.DisabledUntil, "the backoff state did not take")

	svc := &search.Service{
		Client:    c,
		Store:     insertingStore{},
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Zero(t, got.Status.EscalationLevel)
	require.Nil(t, got.Status.DisabledUntil, "a stale disable survived a successful search")
	require.Nil(t, got.Status.InitialFailureAt)
	require.Nil(t, got.Status.LastFailureAt)
	require.Empty(t, got.Status.LastFailure)

	// ... and the rest of the manager's set is still standing.
	require.NotNil(t, got.Status.LastRssAt)
	require.Equal(t, int32(7), got.Status.LastRssNewCount)
	require.Equal(t, int32(3), got.Status.GrabsInWindow)
	require.Equal(t, int64(1236), got.Status.IndexedReleases,
		"indexedReleases must grow by what the store inserted, and by nothing else")
}

// interleavingClient writes to the SAME object, under the SAME field
// manager, WHILE the search is in flight -- exactly as the RSS poll or a
// grab does in production, where a fan-out is seconds long.
type interleavingClient struct {
	t   *testing.T
	c   client.Client
	key client.ObjectKey
	err error
}

func (i interleavingClient) Search(ctx context.Context, _ torznab.Query) ([]torznab.Release, error) {
	var live indexv1alpha1.Indexer
	require.NoError(i.t, i.c.Get(ctx, i.key, &live))

	until := metav1.NewTime(time.Now().Add(30 * time.Minute).Truncate(time.Second))
	rssAt := metav1.NewTime(time.Now().Truncate(time.Second))
	require.NoError(i.t, idxstatus.Patch(ctx, i.c, k8s.ManagerIndexarrWorker, &live,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			st := live.Status
			st.LastRssAt = &rssAt
			st.LastRssNewCount = 42
			st.GrabsInWindow = 9
			st.IndexedReleases = 9999
			// A run of failures another writer recorded. It is the tell:
			// inside the startup grace RecordFailure keeps whatever level
			// it is HANDED, so a fresh read escalates from 3 and a stale
			// one from 0 -- and only one of those disables the indexer.
			st.EscalationLevel = 3
			st.DisabledUntil = &until
			st.InitialFailureAt = &rssAt
			st.LastFailureAt = &rssAt
			st.LastFailure = "http 503"
			*ac = *idxstatus.WorkerFields(st)
		}))
	return nil, i.err
}

// The failure no release-regression test can see.
//
// Both applies declare every field the manager owns; the stale one just
// declares OLDER VALUES. disabledUntil is what turns it from a counter blip
// into a correctness bug: WorkerFields emits it only when non-nil, so a
// snapshot taken before another writer set the backoff does not roll it
// back, it CLEARS it -- silently re-enabling an indexer that was just put
// into backoff and undoing the thing that stops us hammering a failing
// tracker.
//
// The second writer is REAL and runs mid-fan-out, rather than being a
// sequence arranged around the call: a test that writes before or after the
// search cannot observe a lost update at all.
func TestASearchDoesNotRollBackAWriteThatLandedDuringTheFanOut(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-interleave", "nzbgeek")
	key := client.ObjectKeyFromObject(idx)

	svc := &search.Service{
		Client: c,
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return interleavingClient{t: t, c: c, key: key, err: errors.New("tracker exploded")}, nil
		},
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, key, &got))

	require.Equal(t, int32(42), got.Status.LastRssNewCount,
		"the stale snapshot rolled back a write that landed during the fan-out")
	require.Equal(t, int32(9), got.Status.GrabsInWindow, "rolled back by a stale snapshot")
	require.Equal(t, int64(9999), got.Status.IndexedReleases, "rolled back by a stale snapshot")

	// The escalation continued the run the other writer had started rather
	// than beginning a new one from zero. A stale snapshot would have read
	// level 0 here and produced no disable at all -- silently leaving an
	// indexer that has failed four times in a row fully in service.
	require.Equal(t, int32(3), got.Status.EscalationLevel, "the escalation run restarted from a stale level")
	require.NotNil(t, got.Status.DisabledUntil, "the stale snapshot lost the backoff entirely")
	require.NotNil(t, got.Status.InitialFailureAt, "the run's start was lost with the stale snapshot")
}

// status.queriesInWindow is a PROJECTION of the query ring, not a running
// total. The ring is what makes the limit recoverable: an increment with no
// window would pass spec.limits.queryLimit once and skip the indexer for
// good, because nothing in the system ever lowers it.
func TestASearchProjectsTheQueryRingOntoStatus(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-queries", "nzbgeek")

	svc := &search.Service{
		Client:    c,
		Bus:       newBus(t),
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	_ = svc.Search(ctx, searchRequest(idx.Namespace))

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(1), got.Status.QueriesInWindow,
		"the ring is the source of truth, not the previous status value")

	_ = svc.Search(ctx, searchRequest(idx.Namespace))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(2), got.Status.QueriesInWindow)

	// ... and the rest of the manager's set survived both.
	require.NotNil(t, got.Status.LastRssAt)
	require.Equal(t, int32(7), got.Status.LastRssNewCount)
	require.Equal(t, int32(3), got.Status.GrabsInWindow)
}

// An indexer that is skipped is never queried, so nothing about it changes:
// an apply that does not happen can neither release nor roll back.
func TestASkippedIndexerIsNotWrittenAtAll(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-skipped", "nzbgeek")

	// Disable it through the spec, which is the operator's switch.
	idx.Spec.Enabled = new(bool)
	require.NoError(t, c.Update(ctx, idx))

	svc := failingService(c, errors.New("must not be called"))
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeSkipped, resp.Outcomes[0].Status)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Zero(t, got.Status.EscalationLevel)
	require.Equal(t, int32(5), got.Status.QueriesInWindow)
	require.Equal(t, int64(1234), got.Status.IndexedReleases)
	require.NotNil(t, got.Status.LastRssAt)
}

// hangingClient never answers, so the per-indexer deadline is what ends the
// query -- the case the escalation ladder exists for.
type hangingClient struct{}

func (hangingClient) Search(ctx context.Context, _ torznab.Query) ([]torznab.Release, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// An indexer that TIMED OUT must still have its failure recorded.
//
// This is the regression test for the per-indexer timeout bounding only the
// outbound call. If the status apply ran on that same context it would be
// ALREADY EXPIRED on exactly this path: the Get would fail instantly,
// recordOutcome would log a warning and return, and an indexer that hangs on
// every request would never escalate, never be disabled and never stop being
// queried -- while the reply still, misleadingly, said "timeout".
//
// The clock is past idxstatus.StartupGrace so the ladder actually steps; the
// non-escalating in-grace branch would pass whether or not the apply landed.
func TestATimedOutIndexerStillRecordsItsEscalation(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	idx := steadyState(t, ctx, c, "search-timeout", "nzbgeek")

	idx.Spec.Timeout = metav1.Duration{Duration: 100 * time.Millisecond}
	require.NoError(t, c.Update(ctx, idx))

	svc := &search.Service{
		Client: c,
		Now:    pastTheStartupGrace(),
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return hangingClient{}, nil
		},
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeTimeout, resp.Outcomes[0].Status)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(1), got.Status.EscalationLevel,
		"a timed-out indexer never escalated: the status apply ran on the expired query context")
	require.NotNil(t, got.Status.DisabledUntil,
		"a timed-out indexer was never backed off, so it will be hammered on every search")
	require.NotEmpty(t, got.Status.LastFailure, "the timeout was not recorded at all")
	require.NotNil(t, got.Status.InitialFailureAt)

	// ... and the shared manager's other fields survived it.
	require.NotNil(t, got.Status.LastRssAt)
	require.Equal(t, int32(7), got.Status.LastRssNewCount)
	require.Equal(t, int32(3), got.Status.GrabsInWindow)
}
