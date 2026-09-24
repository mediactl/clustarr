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

package indexarr

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/relindex"
)

// countingStore is a relindex.Store stub that only serves Prune, which is
// all indexSweeper calls; every other method panics via the nil embedded
// interface if a test ever reaches it, which is deliberate -- a test that
// needs more than Prune from the store is testing the wrong thing here.
type countingStore struct {
	relindex.Store

	mu     sync.Mutex
	pruned int
}

func (s *countingStore) Prune(context.Context, time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned++
	return 0, nil
}

func (s *countingStore) prunedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruned
}

// TestIndexSweeperIsLeaderElected is ruling R1: "the sweep is leader-only
// under both engines." A Postgres-backed release index (Options.IndexDSN)
// may run several indexarr replicas, and racing Prune calls across them is
// wasted work worth doing exactly once, so the runnable must declare
// manager.LeaderElectionRunnable and answer true -- not be a k8s.EveryReplica
// or a bare func, either of which controller-runtime would start on every
// replica.
func TestIndexSweeperIsLeaderElected(t *testing.T) {
	sw := indexSweeper{store: &countingStore{}}

	ler, ok := any(sw).(manager.LeaderElectionRunnable)
	require.True(t, ok, "indexSweeper must implement manager.LeaderElectionRunnable")
	require.True(t, ler.NeedLeaderElection(),
		"indexSweeper.NeedLeaderElection() must be true (ruling R1): under SQLite the single "+
			"replica is still the leader, so nothing observable changes, but a Postgres-backed "+
			"index with several replicas must run the sweep on exactly one of them")

	var _ manager.Runnable = sw // Start(context.Context) error
}

// TestIndexSweeperPrunesOnceOnStartBeforeTicking proves indexSweeper kept the
// old sweepReleaseIndex behaviour the type replaced: a sweep on startup,
// before the first 10-minute tick (IndexSweepInterval), and a clean stop when
// ctx is cancelled. The test does not wait out a real IndexSweepInterval; it
// only needs the immediate, pre-ticker sweep and prompt cancellation.
func TestIndexSweeperPrunesOnceOnStartBeforeTicking(t *testing.T) {
	store := &countingStore{}
	sw := indexSweeper{store: store}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sw.Start(ctx) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("indexSweeper.Start did not return after its context was cancelled")
	}

	require.GreaterOrEqual(t, store.prunedCount(), 1,
		"indexSweeper.Start must prune once on startup, before the first IndexSweepInterval tick")
}
