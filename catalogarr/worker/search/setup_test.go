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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// addRecorder is a ctrl.Manager that records what SetupWithManager adds.
// Only Add is implemented; anything else panics on the nil embedded value,
// which is the point -- SetupWithManager must need nothing else.
type addRecorder struct {
	ctrl.Manager
	added []manager.Runnable
}

func (a *addRecorder) Add(r manager.Runnable) error {
	a.added = append(a.added, r)
	return nil
}

// subRecorder is an events.Bus that records every subscription.
type subRecorder struct {
	events.Bus
	mu   sync.Mutex
	subs []events.Subscription
}

func (s *subRecorder) Subscribe(_ context.Context, sub events.Subscription, _ events.Handler) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, sub)
	return func() {}, nil
}

// TestSetupWithManagerRunsOnEveryReplicaFromTheGivenTopology pins two
// carried Phase C/D items: the consumers were registered through a private
// copy of k8s.EveryReplica the registration guard could not see, and were
// looked up in events.Default() rather than the topology the process
// installed.
func TestSetupWithManagerRunsOnEveryReplicaFromTheGivenTopology(t *testing.T) {
	topo := events.Default()
	const marker = 7 * time.Second
	for i := range topo.Consumers {
		if topo.Consumers[i].Name == highConsumer {
			topo.Consumers[i].AckWait = marker
		}
	}

	w := &Worker{Topology: &topo}
	mgr := &addRecorder{}
	bus := &subRecorder{}
	require.NoError(t, w.SetupWithManager(mgr, bus))
	require.Len(t, mgr.added, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Start subscribes, then returns once ctx is done.
	for _, r := range mgr.added {
		er, ok := r.(k8s.EveryReplica)
		require.True(t, ok, "each consumer is a k8s.EveryReplica, visible to the registration guard; got %T", r)
		require.False(t, er.NeedLeaderElection())
		require.NoError(t, er.Start(ctx))
	}

	require.Len(t, bus.subs, 2)
	var high *events.Subscription
	for i := range bus.subs {
		if bus.subs[i].Durable == highConsumer {
			high = &bus.subs[i]
		}
	}
	require.NotNil(t, high)
	require.Equal(t, marker, high.AckWait, "the consumer comes from the topology the caller handed in, not events.Default()")
}

// highConsumer is the consumer the topology test perturbs.
const highConsumer = events.ConsumerCatalogSearchHigh
