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

package search

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func testBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	return bus
}

// A nil Bus disables accounting rather than failing the search.
func TestReserveQueryWithoutABus(t *testing.T) {
	s := &Service{}
	idx := healthyIndexer("nobus")
	n, ok := s.reserveQuery(context.Background(), &idx)
	require.Nil(t, n)
	require.True(t, ok)
}

// countingClient answers every search and counts the requests that reached
// it.
type countingClient struct{ calls *atomic.Int32 }

func (c countingClient) Search(context.Context, torznab.Query) ([]torznab.Release, error) {
	c.calls.Add(1)
	return wireReleases(1), nil
}

// The defect this replaced: the fan-out skipped an indexer whose
// status.queriesInWindow had reached spec.limits.queryLimit, and only a
// counted query moved that field -- so an indexer at its limit was never
// asked again, and nothing ever counted again to bring it down. The gate is
// now the ring's own reservation, made the moment before the request: at
// the limit the indexer is skipped without being contacted, and once the
// window has passed -- with no traffic in between -- it is asked again.
func TestAnIndexerAtItsQueryLimitIsAskedAgainOnceTheWindowPasses(t *testing.T) {
	idxs := fanoutIndexers("budget")
	idxs[0].Spec.Limits = &indexv1alpha1.Limits{QueryLimit: ptr.To[int32](1), Unit: indexv1alpha1.LimitUnitHour}
	var calls atomic.Int32
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	s := serviceFor(idxs, nil)
	s.ClientFor = func(context.Context, *indexv1alpha1.Indexer) (IndexerClient, error) {
		return countingClient{calls: &calls}, nil
	}
	s.Bus = testBus(t)
	s.Now = func() time.Time { return clock }

	run := func() schema.SearchOutcome {
		t.Helper()
		cands := selectCandidates(idxs, movieRequest(), torznab.ModeMovieSearch, clock)
		outcomes, _ := s.fanOut(context.Background(), cands, movieRequest(), torznab.ModeMovieSearch, 30*time.Second)
		require.Len(t, outcomes, 1)
		return outcomes[0]
	}

	require.Equal(t, schema.SearchOutcomeOK, run().Status)
	require.EqualValues(t, 1, calls.Load())

	clock = t0.Add(time.Minute)
	out := run()
	require.Equal(t, schema.SearchOutcomeSkipped, out.Status)
	require.Equal(t, skipQueryLimit, out.Error)
	require.EqualValues(t, 1, calls.Load(), "an indexer at its limit must not be contacted")

	w, err := limits.Queries(context.Background(), s.Bus.KV(events.BucketIndexerLimits), &idxs[0], clock)
	require.NoError(t, err)
	require.EqualValues(t, 1, w.Count, "a refused query is not counted")

	clock = t0.Add(time.Hour + time.Second)
	require.Equal(t, schema.SearchOutcomeOK, run().Status, "the window passed with no traffic; the indexer is asked again")
	require.EqualValues(t, 2, calls.Load())
}
