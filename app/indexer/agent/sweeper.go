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

package agent

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// Release-index retention, from §6.2's "`expires_at` sweep every 10 min
// (72h)". [relindex.Store.Prune] is a pure function of its argument and
// schedules nothing on purpose, so the schedule lives here.
const (
	// IndexRetention is how long a release stays in the local index after it
	// was fetched.
	IndexRetention = 72 * time.Hour

	// IndexSweepInterval is how often [relindex.Store.Prune] runs.
	IndexSweepInterval = 10 * time.Minute
)

// indexSweeper is §6.2's "`expires_at` sweep every 10 min (72h)".
// relindex.Open/OpenPostgres starts no goroutines and Prune reads no clock,
// both deliberately, so the schedule is here.
//
// It sweeps once on startup before it starts ticking: indexarr is pinned to a
// Recreate rollout under SQLite, so a restart is the moment the index is most
// likely to be holding a backlog older than the window, and a first sweep ten
// minutes in leaves that backlog answering searches until then.
//
// A failed sweep is logged and the ticker continues. Returning the error would
// take the whole manager down over a transient SQLITE_BUSY, and the index is a
// cache (ADR-0003): the cost of a missed sweep is disk, not correctness.
//
// It is a typed Runnable that states its election rather than leaving it to
// controller-runtime: the sweep runs on every replica of the index domain,
// which is fixed at one under both engines (spec §5.12, superseding ruling
// R1). A prune racing on a second replica would be wasted work, not
// corruption.
type indexSweeper struct {
	store relindex.Store
}

// Start implements manager.Runnable.
func (s indexSweeper) Start(ctx context.Context) error {
	pruneOnce(ctx, s.store)
	ticker := time.NewTicker(IndexSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pruneOnce(ctx, s.store)
		}
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable. It is false
// because the sweep runs in the index agent, which never elects (§3.5.2).
// The domain is fixed at one replica under both engines (§13 OD15), and a
// prune racing on a second replica would be wasted work, not corruption.
// This supersedes ruling R1's "leader-only".
func (indexSweeper) NeedLeaderElection() bool { return false }

// pruneOnce drops every release fetched more than [IndexRetention] ago.
func pruneOnce(ctx context.Context, store relindex.Store) {
	ctx, span := tracing.Start(ctx, "indexarr.relindex.prune")
	defer span.End()

	log := logging.FromContext(ctx)
	deleted, err := store.Prune(ctx, time.Now().Add(-IndexRetention))
	if err != nil {
		tracing.RecordError(span, err)
		log.Error("indexarr: pruning the release index", "error", err)
		return
	}
	if deleted > 0 {
		log.Info("indexarr: pruned the release index",
			"deleted", deleted, "retention", IndexRetention.String())
	}

	// The blocklist's own expiry pass (ADR-0019 §6.14): a row goes at its
	// until, never with the release retention.
	blocks, err := store.PruneBlocks(ctx, time.Now())
	if err != nil {
		tracing.RecordError(span, err)
		log.Error("indexarr: pruning the blocklist", "error", err)
		return
	}
	if blocks > 0 {
		log.Info("indexarr: pruned expired blocklist rows", "deleted", blocks)
	}
}
