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
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/search"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// queryCountingWorker wires the worker exactly as indexarr/run.go does: its
// CountQuery is indexarr/search.CountQuery over the bus's
// clustarr-indexer-limits bucket, the ring the search fan-out counts into.
func queryCountingWorker(t *testing.T, clock *fakeClock, s rss.Searcher, c client.Client, bus events.Bus) *rss.Worker {
	t.Helper()
	kv := bus.KV(events.BucketIndexerLimits)
	return rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  &fakeStore{},
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return s, nil
		},
		CountQuery: func(ctx context.Context, idx *indexv1alpha1.Indexer, now time.Time) (int32, error) {
			return search.CountQuery(ctx, kv, idx, now)
		},
		Clock: clock.Now,
	})
}

// Every page an RSS poll requests counts toward the query window, into the
// SAME ring a search counts into -- Prowlarr counts IndexerQuery and
// IndexerRss together against QueryLimit. Before, a poll counted nothing, so
// a polled-and-searched indexer's queriesInWindow under-reported its traffic
// by up to four requests every poll.
func TestPollCountsEveryPageIntoTheSearchQueryRing(t *testing.T) {
	clock := newFakeClock(t0)
	bus := newTestBus(t)
	idx := testIndexer("media", "idx")
	c := newFakeClient(idx)

	// A search already spent one query in this window.
	n, err := search.CountQuery(t.Context(), bus.KV(events.BucketIndexerLimits), idx, t0)
	require.NoError(t, err)
	require.Equal(t, int32(1), n)

	searcher := &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
		return pageOf(100), nil // full pages, so the poll reads all four
	}}
	w := queryCountingWorker(t, clock, searcher, c, bus)
	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))
	require.Equal(t, 4, searcher.calls())

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(5), got.Status.QueriesInWindow, "one search plus four RSS pages")
	require.NotNil(t, got.Status.LastRssAt, "the rest of the poll's status still lands")
}

// A page that fails still reached the indexer and still spends its budget, so
// the failure path counts it too, in the same one apply as the escalation.
func TestAFailedPollStillCountsTheRequestItMade(t *testing.T) {
	clock := newFakeClock(tNow)
	bus := newTestBus(t)
	idx := testIndexer("media", "idx", func(i *indexv1alpha1.Indexer) { i.Status.QueriesInWindow = 7 })
	c := newFakeClient(idx)

	w := queryCountingWorker(t, clock, &fakeSearcher{err: errors.New("connection refused")}, c, bus)
	require.Error(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(1), got.Status.QueriesInWindow,
		"the ring is the source of truth: one request in this window, whatever the stale projection said")
	require.NotNil(t, got.Status.LastFailureAt, "the failure is recorded in the same apply")
}
