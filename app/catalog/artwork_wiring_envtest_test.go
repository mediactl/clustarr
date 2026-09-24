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

package catalogarr

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// subscriptionRecorder is a bus that records every durable subscribed on it.
type subscriptionRecorder struct {
	events.Bus
	mu       sync.Mutex
	durables []string
}

func (b *subscriptionRecorder) Subscribe(ctx context.Context, s events.Subscription, h events.Handler) (func(), error) {
	b.mu.Lock()
	b.durables = append(b.durables, s.Durable)
	b.mu.Unlock()
	return b.Bus.Subscribe(ctx, s, h)
}

func (b *subscriptionRecorder) subscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.durables)
}

// TestTheRenderConsumerRunsUnderArtworkAndAllOnly is the role half of the
// registration guard for spec §C.6: `catalogarr --role artwork` subscribes
// catalogarr-artwork-render, `--role all` (and so `clustarr all`) does
// too, and no other role drags it in. cmd/clustarr's start envtest runs
// both on a real bus under catalogarr's RBAC and watches a render land.
func TestTheRenderConsumerRunsUnderArtworkAndAllOnly(t *testing.T) {
	requireEnvtest(t)
	for _, tc := range []struct {
		role   Role
		render bool
	}{
		{RoleArtwork, true},
		{RoleAll, true},
		{"worker,history,artwork", true},
		{RoleWorker, false},
		{RoleMetadata, false},
		{RoleHistory, false},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			mgr := newManager(t)
			bus := &subscriptionRecorder{Bus: newBus(t)}
			require.NoError(t, setupWorkers(mgr, bus, Options{Role: tc.role}))
			startManager(t, mgr)

			require.Eventually(t, func() bool { return len(bus.subscribed()) > 0 }, 30*time.Second, 10*time.Millisecond,
				"role %s subscribed nothing", tc.role)
			if tc.render {
				require.Eventually(t, func() bool {
					return slices.Contains(bus.subscribed(), events.ConsumerCatalogArtworkRender)
				}, 30*time.Second, 10*time.Millisecond, "role %s never subscribed %s: %v",
					tc.role, events.ConsumerCatalogArtworkRender, bus.subscribed())
				return
			}
			time.Sleep(500 * time.Millisecond)
			assert.NotContains(t, bus.subscribed(), events.ConsumerCatalogArtworkRender)
		})
	}
}
