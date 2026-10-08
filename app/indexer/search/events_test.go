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

// The search fan-out announces the one transition only it can see: the
// query whose reservation fills spec.limits.queryLimit publishes §5's
// indexer.limited, once. Since ADR-0019 A6.3 the escalation ladder runs in
// the Indexer controller, which publishes disabled and recovered after its
// incorporation apply (TestTheLadderRunsInTheController), so a failed query
// here announces nothing.
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

	// A failure is a health record, not an announcement: the controller
	// decides whether it disables the indexer.
	s.Search(ctx, movieRequest())
	require.Never(t, func() bool { return len(received()) > 0 }, 500*time.Millisecond, 10*time.Millisecond,
		"the agent announced a transition the controller's ladder owns")

	// The indexer answers, and the second query fills the queryLimit of 2:
	// limited, once.
	clock = clock.Add(2 * time.Minute)
	cli.failing.Store(false)
	s.Search(ctx, movieRequest())
	require.Eventually(t, func() bool { return len(received()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{events.ActionLimited}, actions())
	require.Equal(t, "uid-flaky", received()[0].IndexerRef.UID)

	// A further healthy query changes nothing worth announcing.
	clock = clock.Add(time.Minute)
	s.Search(ctx, movieRequest())
	require.Never(t, func() bool { return len(received()) > 1 }, 500*time.Millisecond, 10*time.Millisecond)
}
