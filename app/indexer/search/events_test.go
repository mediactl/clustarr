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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// flakyClient fails while failing is set and answers otherwise.
type flakyClient struct{ failing atomic.Bool }

func (c *flakyClient) Search(context.Context, torznab.Query) ([]torznab.Release, error) {
	if c.failing.Load() {
		return nil, errors.New("connection refused")
	}
	return wireReleases(1), nil
}

func historyEvents(t *testing.T, bus events.Bus) func() []schema.IndexerEvent {
	t.Helper()
	spec, ok := events.Default().ForSingleNode().Consumer(events.ConsumerCatalogHistory)
	require.True(t, ok)
	var (
		mu  sync.Mutex
		got []schema.IndexerEvent
	)
	stop, err := bus.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		var p schema.IndexerEvent
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &p); err != nil {
			return nil // another producer's event
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return func() []schema.IndexerEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]schema.IndexerEvent(nil), got...)
	}
}

// The search fan-out is where most escalations are applied, so it is where
// §5's indexer.disabled|recovered|limited events come from: a failure that
// opens a backoff window announces it, the first success after it announces
// the recovery, and the query that fills spec.limits.queryLimit announces the
// limit -- each once, after the status apply.
func TestTheFanOutPublishesIndexerTransitions(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	received := historyEvents(t, bus)

	idx := healthyIndexer("flaky")
	idx.UID = "uid-flaky"
	idx.Spec.Limits = &indexv1alpha1.Limits{QueryLimit: ptr.To[int32](2)}
	c := newFakeClient(&idx)
	cli := &flakyClient{}
	cli.failing.Store(true)
	// Past the ladder's startup grace, so a failure escalates at all.
	clock := time.Now().Add(time.Hour)
	s := &Service{
		Client: c, Bus: bus, Now: func() time.Time { return clock },
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (IndexerClient, error) { return cli, nil },
	}

	actions := func() []string {
		var out []string
		for _, e := range received() {
			out = append(out, e.Action)
		}
		return out
	}

	s.Search(ctx, movieRequest())
	require.Eventually(t, func() bool { return len(received()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{events.ActionDisabled}, actions())
	dis := received()[0]
	require.Equal(t, "uid-flaky", dis.IndexerRef.UID)
	require.Contains(t, dis.Reason, "connection refused")
	require.NotNil(t, dis.Until)

	// The window lapses and the indexer answers: recovered, and the second
	// query fills the queryLimit of 2, so limited too.
	clock = clock.Add(2 * time.Minute)
	cli.failing.Store(false)
	s.Search(ctx, movieRequest())
	require.Eventually(t, func() bool { return len(received()) == 3 }, 5*time.Second, 10*time.Millisecond)
	require.ElementsMatch(t, []string{events.ActionDisabled, events.ActionRecovered, events.ActionLimited}, actions())

	// A further healthy query changes nothing worth announcing.
	clock = clock.Add(time.Minute)
	s.Search(ctx, movieRequest())
	require.Never(t, func() bool { return len(received()) > 3 }, 500*time.Millisecond, 10*time.Millisecond)
}
