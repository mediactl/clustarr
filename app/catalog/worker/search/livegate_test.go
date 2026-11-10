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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestTheLiveGateAdmitsAtMostItsSize(t *testing.T) {
	g := newLiveGate(2)
	ctx := context.Background()
	r1, err := g.acquire(ctx, false)
	require.NoError(t, err)
	_, err = g.acquire(ctx, false)
	require.NoError(t, err)

	third := make(chan func(), 1)
	go func() {
		r, err := g.acquire(ctx, false)
		if err == nil {
			third <- r
		}
	}()
	require.Eventually(t, func() bool { return g.queued() == 1 }, time.Second, time.Millisecond)
	select {
	case <-third:
		t.Fatal("a third search ran beside two on a gate of two")
	case <-time.After(20 * time.Millisecond):
	}
	r1()
	r1() // a release is idempotent: it frees one place, once
	select {
	case r := <-third:
		r()
	case <-time.After(time.Second):
		t.Fatal("the waiting search was not admitted when a place freed")
	}
	require.Equal(t, 0, g.queued())
}

// A person's search waits behind the running ones, never behind the
// automatic ones queued before it.
func TestTheLiveGateAdmitsAPersonsSearchFirst(t *testing.T) {
	g := newLiveGate(1)
	ctx := context.Background()
	running, err := g.acquire(ctx, false)
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	wait := func(name string, user bool) {
		defer wg.Done()
		r, err := g.acquire(ctx, user)
		if err != nil {
			return
		}
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		r()
	}
	wg.Add(1)
	go wait("automatic", false)
	require.Eventually(t, func() bool { return g.queued() == 1 }, time.Second, time.Millisecond)
	wg.Add(1)
	go wait("person", true)
	require.Eventually(t, func() bool { return g.queued() == 2 }, time.Second, time.Millisecond)

	running()
	wg.Wait()
	require.Equal(t, []string{"person", "automatic"}, order)
}

func TestACancelledWaitTakesNoPlace(t *testing.T) {
	g := newLiveGate(1)
	r, err := g.acquire(context.Background(), false)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, false)
		done <- err
	}()
	require.Eventually(t, func() bool { return g.queued() == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, 0, g.queued())

	r()
	got, err := g.acquire(context.Background(), false)
	require.NoError(t, err, "the place the cancelled waiter never took is free")
	got()
}

func TestANilGateAdmitsEverything(t *testing.T) {
	require.Nil(t, newLiveGate(0))
	var g *liveGate
	for range 10 {
		r, err := g.acquire(context.Background(), false)
		require.NoError(t, err)
		defer r()
	}
}

// blockingRPC answers a search only once released, counting how many are
// in flight at once.
type blockingRPC struct {
	release        chan struct{}
	mu             sync.Mutex
	inFlight, peak int
}

func (b *blockingRPC) Search(ctx context.Context, _ schema.SearchRequest) (schema.SearchResponse, error) {
	b.mu.Lock()
	b.inFlight++
	b.peak = max(b.peak, b.inFlight)
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	select {
	case <-b.release:
	case <-ctx.Done():
		return schema.SearchResponse{}, ctx.Err()
	}
	return schema.SearchResponse{}, nil
}

type gateMessage struct{ events.Message }

func (gateMessage) InProgress(context.Context) error { return nil }

func TestLiveSearchesRunNoMoreThanTheWorkersCap(t *testing.T) {
	rpc := &blockingRPC{release: make(chan struct{})}
	w := &Worker{RPC: rpc, LiveSearches: 2}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, err := w.searchLive(context.Background(), gateMessage{}, schema.SearchTask{}, schema.SearchRequest{})
			assert.NoError(t, err)
		})
	}
	require.Eventually(t, func() bool { return w.liveGate().queued() == 3 }, time.Second, time.Millisecond)
	close(rpc.release)
	wg.Wait()
	require.Equal(t, 2, rpc.peak, "two live searches at once, never more")
}
