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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	searchworker "github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/app/indexer/health"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/app/indexer/search"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/app/indexer/status/statuspatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
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
// state under both of the Indexer controller's managers, and its
// clustarr-indexer-health record to a completed RSS poll (lastRssAt,
// lastRssNewCount, indexedReleases), exactly as app/indexer/worker/rss would
// leave it (ADR-0019 A6.3: the index agent reports outcomes in the record
// and writes no Indexer status).
//
// A test that skips this and acts on a blank object or an absent record
// CANNOT observe a lost field, because there is nothing to lose.
func steadyState(
	t *testing.T, ctx context.Context, c client.Client, bus events.Bus, hw *health.Writer, ns, name string,
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

	// The Indexer controller's two halves, each under its own manager. The
	// search path must leave every one of them alone: the agent writes no
	// status at all.
	require.NoError(t, statuspatch.Patch(ctx, c, k8s.ManagerIndex, idx,
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
				// The window counts are the reconciler's projection of the
				// rings (since 2026-10-01): a search must leave them alone.
				GrabsInWindow:   3,
				QueriesInWindow: 5,
			})
			ac.WithConditions(k8s.ConditionACs([]metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionTrue,
				Reason: "Probed", Message: "caps probed",
			}})...)
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))

	rssAt := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	require.NoError(t, statuspatch.Patch(ctx, c, k8s.ManagerIndexWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			*ac = *idxstatus.WorkerFields(indexv1alpha1.IndexerStatus{
				LastRssAt:       &rssAt,
				LastRssNewCount: 7,
				IndexedReleases: 1234,
			})
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.Equal(t, int32(5), idx.Status.QueriesInWindow, "the steady state did not take")
	require.NotNil(t, idx.Status.Caps, "the steady state did not take")

	// The agent's own half: a completed RSS poll in the health record.
	require.NoError(t, hw.RSS(ctx, indexerRef(idx), rssAt.Time, 7, true, "", ""))
	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int32(7), rec.LastRssNewCount, "the steady state did not take")
	return idx
}

// healthKit is one agent's health writer over a fresh bus, as
// app/indexer/agent builds it (newHealthWriter).
func healthKit(t *testing.T) (events.Bus, *health.Writer) {
	t.Helper()
	bus := newBus(t)
	return bus, &health.Writer{
		Records:  records.NewWriter(healthKV(bus), agentrecords.IndexerHealth(), "index-test", "test"),
		Suppress: &health.Suppress{},
	}
}

func healthKV(bus events.Bus) events.KV { return bus.KV(events.BucketIndexerHealth) }

func indexerRef(idx *indexv1alpha1.Indexer) schema.ItemRef {
	return schema.ItemRef{
		Kind: health.IndexerKind,
		Ref:  schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
	}
}

// readHealth reads idx's clustarr-indexer-health record; ok is false when
// none was ever written.
func readHealth(t *testing.T, ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer) (*schema.IndexerHealthRecord, bool) {
	t.Helper()
	rec, _, ok, err := records.NewReader(kv, agentrecords.IndexerHealth()).
		Get(ctx, agentrecords.IndexerHealthKey(string(idx.UID)))
	require.NoError(t, err)
	return rec, ok
}

func healthRecord(t *testing.T, ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer) *schema.IndexerHealthRecord {
	t.Helper()
	rec, ok := readHealth(t, ctx, kv, idx)
	require.True(t, ok, "no health record for %s/%s", idx.Namespace, idx.Name)
	return rec
}

func failingService(c client.Client, hw *health.Writer, err error) *search.Service {
	return &search.Service{
		Client: c,
		Health: hw,
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return errClient{err: err}, nil
		},
	}
}

// insertingStore reports every row it is given as newly inserted, which is
// what the record's indexedReleases counts.
type insertingStore struct{ relindex.Store }

func (insertingStore) Upsert(_ context.Context, rels []relindex.Release) (int, error) {
	return len(rels), nil
}

// BlockState holds no block, so the answer marks no release blocked
// (ADR-0019 §6.14: every answer reads the blocklist).
func (insertingStore) BlockState(context.Context, []relindex.BlockKey, time.Time) (map[int][]relindex.Block, error) {
	return nil, nil
}

type errClient struct{ err error }

func (e errClient) Search(context.Context, torznab.Query) ([]torznab.Release, error) {
	return nil, e.err
}

// requireStatusUntouched holds every leaf the steady state wrote: the search
// path writes no Indexer status (ADR-0019 A6.3), so not one of them moves.
// Every leaf of caps, not just the parent: server-side apply tracks
// ownership per leaf inside a struct.
func requireStatusUntouched(t *testing.T, got indexv1alpha1.Indexer) {
	t.Helper()
	require.Equal(t, int64(1), got.Status.ObservedGeneration)
	require.Equal(t, commonv1.ProtocolUsenet, got.Status.Protocol)
	require.Equal(t, "private", got.Status.Privacy)
	require.Equal(t, "nzbgeek-session", got.Status.SessionSecretRef)
	require.Len(t, got.Status.Conditions, 1)
	require.NotNil(t, got.Status.Caps)
	require.Equal(t, map[string][]string{"movie": {"imdbid", "q", "tmdbid"}}, got.Status.Caps.Modes)
	require.Equal(t, []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}}, got.Status.Caps.Categories)
	require.Equal(t, int32(100), got.Status.Caps.LimitsMax)
	require.Equal(t, int32(50), got.Status.Caps.LimitsDefault)
	require.False(t, got.Status.Caps.SupportsRawSearch)
	require.Equal(t, int32(3), got.Status.GrabsInWindow)
	require.Equal(t, int32(5), got.Status.QueriesInWindow)
	require.NotNil(t, got.Status.LastRssAt)
	require.Equal(t, int32(7), got.Status.LastRssNewCount)
	require.Equal(t, int64(1234), got.Status.IndexedReleases)
	require.Zero(t, got.Status.EscalationLevel)
	require.Nil(t, got.Status.DisabledUntil)
	require.Empty(t, got.Status.LastFailure)
}

// The health record is one read-modify-write shared by the search fan-out
// and the RSS poll, so a failure the search reports must keep what the poll
// recorded. (Before A6.3 this was the same hazard on Indexer.status under
// one shared field manager; the escalation the failure drives is the
// Indexer controller's ladder now, TestTheLadderRunsInTheController.)
func TestAFailingSearchDoesNotReleaseTheRSSFields(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-ssa", "nzbgeek")

	svc := failingService(c, hw, errors.New("tracker exploded"))
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

	rec := healthRecord(t, ctx, healthKV(bus), idx)

	// The failure landed ...
	require.Equal(t, int64(1), rec.Failures)
	require.NotNil(t, rec.LastFailureAt)
	require.Equal(t, schema.IndexerFailureError, rec.LastFailureClass)
	require.Contains(t, rec.LastFailure, "tracker exploded")

	// ... and NOTHING the poll recorded was lost.
	require.NotNil(t, rec.LastRssAt, "lastRssAt was lost by the failure's write")
	require.Equal(t, int32(7), rec.LastRssNewCount, "lost by the failure's write")
	require.Equal(t, int64(7), rec.IndexedReleases, "lost by the failure's write")
	require.Equal(t, int64(1), rec.Successes, "the poll's success was lost by the failure's write")
}

// The mirror: every field of Indexer.status, under either of the
// controller's managers, is untouched by a search. The agent reports
// outcomes and writes no status (ADR-0019 A6.3).
func TestASearchDoesNotTouchTheReconcilersFields(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-split", "nzbgeek")

	svc := failingService(c, hw, errors.New("tracker exploded"))
	_ = svc.Search(ctx, searchRequest(idx.Namespace))

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	requireStatusUntouched(t, got)
}

// A successful search after a run of failures reports the success and what
// it indexed, and keeps the failure history: counters only grow, so the
// Indexer controller can tell a recovery from a record it has not seen.
// Clearing the escalation is the controller's ladder now
// (TestTheLadderRunsInTheController).
func TestASuccessfulSearchClearsTheEscalation(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-recover", "nzbgeek")

	// A failure first, the way a run of failures would leave the record.
	require.NoError(t, hw.Outcome(ctx, indexerRef(idx), false, schema.IndexerFailureError, "tracker exploded", 0))
	failed := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int64(1), failed.Failures, "the failure did not take")

	svc := &search.Service{
		Client:    c,
		Health:    hw,
		Store:     insertingStore{},
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	// The failure armed the local suppression; the manager's verdict would
	// end it. Let it lapse so the search queries the indexer.
	hw.Suppress.Decided(idx.UID, failed.LastFailureAt.Add(time.Second))
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)

	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int64(2), rec.Successes, "the poll's success and the search's")
	require.NotNil(t, rec.LastSuccessAt)
	require.False(t, rec.LastSuccessAt.Before(*rec.LastFailureAt), "the success is not the latest outcome")
	require.Equal(t, int64(1), rec.Failures, "the failure history was lost")

	// ... and the rest of the record is still standing.
	require.NotNil(t, rec.LastRssAt)
	require.Equal(t, int32(7), rec.LastRssNewCount)
	require.Equal(t, int64(7+2), rec.IndexedReleases,
		"indexedReleases must grow by what the store inserted, and by nothing else")
}

// interleavingClient reports an RSS poll through a SECOND health writer on
// the same bucket WHILE the search is in flight -- a poll landing during a
// fan-out that is seconds long. A second writer, not the shared one, so the
// race is the bucket's compare-and-swap rather than one process's.
type interleavingClient struct {
	t   *testing.T
	hw  *health.Writer
	idx *indexv1alpha1.Indexer
	err error
}

func (i interleavingClient) Search(ctx context.Context, _ torznab.Query) ([]torznab.Release, error) {
	require.NoError(i.t, i.hw.RSS(ctx, indexerRef(i.idx), time.Now(), 42, true, "", ""))
	return nil, i.err
}

// The failure no release-regression test can see: a write seeded from a
// read taken before another writer's landed rolls it back. The record is a
// compare-and-swap (records.Writer.Update), so the search's failure is
// decided again on the newer record. The second writer is REAL and runs
// mid-fan-out: a test that writes before or after the search cannot observe
// a lost update at all.
func TestASearchDoesNotRollBackAWriteThatLandedDuringTheFanOut(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-interleave", "nzbgeek")

	other := &health.Writer{
		Records: records.NewWriter(healthKV(bus), agentrecords.IndexerHealth(), "index-other", "test"),
	}
	svc := &search.Service{
		Client: c,
		Health: hw,
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return interleavingClient{t: t, hw: other, idx: idx, err: errors.New("tracker exploded")}, nil
		},
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int32(42), rec.LastRssNewCount,
		"the stale snapshot rolled back a write that landed during the fan-out")
	require.Equal(t, int64(7+42), rec.IndexedReleases, "rolled back by a stale snapshot")
	require.Equal(t, int64(2), rec.Successes, "rolled back by a stale snapshot")
	require.Equal(t, int64(1), rec.Failures, "the search's failure was lost")
	require.Contains(t, rec.LastFailure, "tracker exploded")
}

// A search counts its query on the ring and nowhere else.
// status.queriesInWindow is the Indexer reconciler's projection of the ring
// (it owns the field since 2026-10-01), and the agent writes no status at
// all (A6.3): no index-worker apply may come from a search.
func TestASearchCountsOnTheRingAndLeavesTheProjectionAlone(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-queries", "nzbgeek")

	svc := &search.Service{
		Client:    c,
		Health:    hw,
		Bus:       bus,
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	_ = svc.Search(ctx, searchRequest(idx.Namespace))
	_ = svc.Search(ctx, searchRequest(idx.Namespace))

	w, err := limits.Queries(ctx, bus.KV(events.BucketIndexerLimits), idx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int32(2), w.Count, "each search reserves one query on the ring")

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	requireStatusUntouched(t, got)
	for _, mf := range got.ManagedFields {
		if mf.Manager == k8s.ManagerIndexWorker.String() && mf.FieldsV1 != nil {
			require.NotContains(t, mf.FieldsV1.GetRawString(), "f:queriesInWindow", "the worker manager claimed queriesInWindow")
			require.NotContains(t, mf.FieldsV1.GetRawString(), "f:grabsInWindow", "the worker manager claimed grabsInWindow")
		}
	}
}

// racingKV is the health bucket with a second writer in the one window a
// re-read cannot close: between the read the outcome is seeded from and its
// write. On its first Get it lets the read return and then reports another
// search's outcome -- 100 newly indexed releases -- through a second writer.
type racingKV struct {
	events.KV
	t     *testing.T
	other *health.Writer
	idx   *indexv1alpha1.Indexer
	raced bool
}

func (r *racingKV) Get(ctx context.Context, key string) (events.Entry, error) {
	e, err := r.KV.Get(ctx, key)
	if err != nil {
		return e, err
	}
	if !r.raced {
		r.raced = true
		require.NoError(r.t, r.other.Outcome(ctx, indexerRef(r.idx), true, "", "", 100))
	}
	return e, nil
}

// Two outcome writers on one Indexer lose no increment: the record's write
// is a compare-and-swap, redone from a fresh read, so an outcome landing
// between the read and the write is not overwritten with a count computed
// before it.
func TestTwoOutcomeWritersLoseNoIndexedReleases(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-cas", "nzbgeek")

	kv := healthKV(bus)
	racing := &racingKV{
		KV: kv, t: t, idx: idx,
		other: &health.Writer{Records: records.NewWriter(kv, agentrecords.IndexerHealth(), "index-other", "test")},
	}
	svc := &search.Service{
		Client:    c,
		Health:    &health.Writer{Records: records.NewWriter(racing, agentrecords.IndexerHealth(), "index-test", "test")},
		Store:     insertingStore{},
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)
	require.True(t, racing.raced, "the second writer never ran")

	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int64(7+100+2), rec.IndexedReleases,
		"an increment was lost between the outcome's read and its write")
}

// And without a contrived window: concurrent searches, every one of which
// indexes two new releases, all count.
func TestConcurrentSearchesLoseNoIndexedReleases(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-concurrent", "nzbgeek")

	svc := &search.Service{
		Client:    c,
		Health:    hw,
		Store:     insertingStore{},
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	const searches = 4
	var wg sync.WaitGroup
	for range searches {
		wg.Go(func() { _ = svc.Search(ctx, searchRequest(idx.Namespace)) })
	}
	wg.Wait()

	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int64(7+2*searches), rec.IndexedReleases)
	require.Equal(t, int64(1+searches), rec.Successes)
}

// An indexer that is skipped is never queried, so nothing about it changes:
// a report that does not happen can neither lose nor roll back anything.
func TestASkippedIndexerIsNotWrittenAtAll(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-skipped", "nzbgeek")
	before := healthRecord(t, ctx, healthKV(bus), idx)

	// Disable it through the spec, which is the operator's switch.
	idx.Spec.Enabled = new(bool)
	require.NoError(t, c.Update(ctx, idx))

	svc := failingService(c, hw, errors.New("must not be called"))
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeSkipped, resp.Outcomes[0].Status)

	require.Equal(t, before, healthRecord(t, ctx, healthKV(bus), idx), "a skipped indexer's record was written")
	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	requireStatusUntouched(t, got)
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
// outbound call. If the record's write ran on that same context it would be
// ALREADY EXPIRED on exactly this path: the read would fail instantly, the
// outcome would be logged and dropped, and an indexer that hangs on every
// request would never reach the Indexer controller's ladder, never be
// disabled and never stop being queried -- while the reply still,
// misleadingly, said "timeout".
func TestATimedOutIndexerStillRecordsItsEscalation(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	bus, hw := healthKit(t)
	idx := steadyState(t, ctx, c, bus, hw, "search-timeout", "nzbgeek")

	idx.Spec.Timeout = metav1.Duration{Duration: 100 * time.Millisecond}
	require.NoError(t, c.Update(ctx, idx))

	svc := &search.Service{
		Client: c,
		Health: hw,
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			return hangingClient{}, nil
		},
	}
	resp := svc.Search(ctx, searchRequest(idx.Namespace))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeTimeout, resp.Outcomes[0].Status)

	rec := healthRecord(t, ctx, healthKV(bus), idx)
	require.Equal(t, int64(1), rec.Failures,
		"a timed-out indexer's failure was never recorded: the write ran on the expired query context")
	require.Equal(t, schema.IndexerFailureTimeout, rec.LastFailureClass)
	require.NotEmpty(t, rec.LastFailure, "the timeout was not recorded at all")
	require.NotNil(t, rec.LastFailureAt)

	// ... and the poll's fields survived it.
	require.NotNil(t, rec.LastRssAt)
	require.Equal(t, int32(7), rec.LastRssNewCount)
}
