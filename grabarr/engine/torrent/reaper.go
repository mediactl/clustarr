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

package torrent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// DefaultReapInterval is how often [Reaper] compares the client's own
	// transfer list against this replica's Downloads. Plan task D2-8b: the
	// reaper is level-driven, so the interval only decides how promptly an
	// orphan is noticed, never whether one is -- a missed tick self-heals on
	// the next one.
	DefaultReapInterval = 2 * time.Minute

	// DefaultOrphanGrace is how long a transfer must be observed with no
	// matching Download, continuously, before [Reaper] treats it as an
	// orphan rather than a transfer this replica added a moment ago and has
	// not yet matched.
	//
	// It must comfortably exceed the slowest path from Add to a matched
	// Download: [Reconciler.add] resolves the payload (an HTTP fetch or an
	// indexarr RPC), calls Add, persists the descriptor, then Gets and
	// applies telemetry that sets status.downloadID -- all inside one
	// reconcile, bounded by this package's own
	// controller.Options.ReconciliationTimeout of 5 minutes
	// (SetupWithManager). Ten minutes is double that bound.
	DefaultOrphanGrace = 10 * time.Minute
)

// cacheSyncWaiter is the one method of
// sigs.k8s.io/controller-runtime/pkg/cache.Cache this package needs.
// Accepting the narrow method instead of a full cache.Cache or a
// ctrl.Manager keeps constructing and testing a [Reaper] independent of a
// running manager: D2-8's wiring passes mgr.GetCache(); a test passes a
// stub that returns true immediately.
type cacheSyncWaiter interface {
	WaitForCacheSync(ctx context.Context) bool
}

// Reaper recovers a class of bug none of D2-4, D2-5 or D2-6 caused alone:
// D2-4's Download controller finalizer removes files and drops the
// finalizer without waiting for any engine (its doc.go, "the finalizer
// needs no live engine" -- true for disk, not for client state), so a
// Download deleted while this replica's watch has not yet delivered the
// deletion -- this engine was down, the deletion was processed during
// re-attach, or the watch event was simply missed -- leaves its transfer
// running in [Engine.Client] forever: a torrent goes on seeding with
// nothing in any CR to say so.
//
// It is deliberately level-driven rather than triggered by the delete
// event that [Reconciler.reconcileDeleting] handles: on a timer, it lists
// every transfer [Engine.Client] currently holds and every Download this
// replica's cache currently holds labelled for [Reaper.EngineID], and
// removes any transfer with no matching Download. Reconciling list against
// list recovers from all three cases above because it never depends on
// having observed the event that caused the loss in the first place --
// unlike an edge-triggered handshake, which fails exactly those cases.
//
// # Orphan detection is conservative by construction, on two independent axes
//
// Guard one: [Reaper.Cache], when set, gates the whole loop on
// WaitForCacheSync. Before the informer backing [Reaper.Client] has done its
// initial List, a List call can return an EMPTY result even though Downloads
// exist -- pkg/k8s.CacheSyncChecker's own doc comment describes exactly this
// failure mode for a fresh replica. Without this gate, every live transfer
// would look orphaned to a Reaper that starts reaping before its cache has
// populated even once.
//
// Guard two, independent of the first: a transfer with no matching Download
// is not reaped on the pass it is first seen unmatched. [Reaper] remembers,
// in memory, the first tick each transfer id was seen unmatched and only
// removes it once that has held continuously for [Reaper.OrphanGrace] --
// see [DefaultOrphanGrace] for why that duration is sized against this
// package's own slowest Add-to-match path. This catches the case cache-sync
// alone cannot: a fully synced cache that simply has not yet been told about
// a Download this same replica added moments ago (an Add can succeed before
// the follow-up status apply that records status.downloadID lands, or before
// this replica's own watch delivers that write back to its cache). A
// transfer flips back to "matched" the instant it appears in a Download's
// status.downloadID, which resets its clock, so a slow-but-legitimate Add
// is never at risk once it lands.
//
// # deleteData is always false
//
// By the time a transfer is old enough to be reaped, its Download is gone
// from the apiserver entirely -- there is no spec.removeDataOnDelete left to
// read, and D2-4's finalizer already ran fsops.SafeRemove against
// status.outputPath when spec asked for it. Passing deleteData=true here
// would ask [Engine.Client] to delete files the controller either already
// removed (redundant, and this package cannot know whether the engine
// treats a missing file as success) or deliberately left in place (a
// straight violation of removeDataOnDelete=false, which this reaper has no
// way to re-derive once the object is gone). The reaper's job is narrower
// than the finalizer's: stop the client from seeding or otherwise holding
// the transfer open, not manage disk state a controller already decided.
//
// # Field manager
//
// Reaper writes nothing to Download.status and touches no
// metadata.finalizers; it only calls [download.Client.Remove]. It shares no
// state with [Reconciler] beyond the embedded [Engine], and does not
// require Reconcile to have run for a given Download at all.
type Reaper struct {
	// Client lists Downloads. In production this is the manager's
	// cache-backed client (the same one [Reconciler.Client] is), so
	// [Reaper.Cache] should be the same manager's cache.
	Client client.Client

	// Engine wraps the embedded download.Client and the re-attach gate.
	// [Reaper.ReapOnce] refuses to run while !Engine.Ready(), the same gate
	// [Reconciler.Reconcile] enforces (R4): reaping against a client that
	// has not finished loading its own persisted transfers would see an
	// incomplete list and could remove a transfer that re-attach has not
	// gotten to yet.
	Engine *Engine

	// EngineID is this pod's "<client>-<ordinal>" identity
	// (grabarr.Options.Engine) -- the same value [Reconciler.EngineID]
	// holds and the same label value Downloads for this replica carry.
	EngineID string

	// Cache gates the reap loop on the manager's informer cache having
	// synced at least once. Nil skips the wait -- tests only; D2-8's wiring
	// must set this to mgr.GetCache(), or this guard does nothing.
	Cache cacheSyncWaiter

	// ReapInterval overrides [DefaultReapInterval].
	ReapInterval time.Duration

	// OrphanGrace overrides [DefaultOrphanGrace].
	OrphanGrace time.Duration

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	mu             sync.Mutex
	unmatchedSince map[string]time.Time
}

// NeedLeaderElection makes the reaper run on every replica rather than only
// the leader, matching pkg/k8s.EveryReplica's own reasoning: each engine
// replica embeds its own [Engine.Client] and knows only its own transfers,
// so a leader-elected singleton would see, at most, one replica's client and
// leave every other replica's orphans unreaped.
func (r *Reaper) NeedLeaderElection() bool { return false }

func (r *Reaper) reapInterval() time.Duration {
	if r.ReapInterval > 0 {
		return r.ReapInterval
	}
	return DefaultReapInterval
}

func (r *Reaper) orphanGrace() time.Duration {
	if r.OrphanGrace > 0 {
		return r.OrphanGrace
	}
	return DefaultOrphanGrace
}

func (r *Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Start implements manager.Runnable. It blocks on [Reaper.Cache]'s
// WaitForCacheSync before the first tick, then reaps on [Reaper.ReapInterval]
// until ctx is done. It returns nil on cancellation, matching every other
// Runnable in this tree (e.g. catalogarr/controller/wantedcron): a Runnable
// that returns an error takes the whole manager down with it, and a
// graceful shutdown is not an error.
func (r *Reaper) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("runnable", "torrent-reaper", "engine", r.EngineID)

	if r.Cache != nil && !r.Cache.WaitForCacheSync(ctx) {
		// Only reachable when ctx is already done (manager shutting down);
		// returning an error here would fail the whole manager during a
		// graceful stop, same reasoning as pkg/k8s.CacheSyncChecker.
		return nil
	}

	ticker := time.NewTicker(r.reapInterval())
	defer ticker.Stop()
	log.InfoContext(ctx, "torrent: orphan reaper started", "interval", r.reapInterval(), "grace", r.orphanGrace())
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.tick(ctx, log)
		}
	}
}

// tick runs one [Reaper.ReapOnce] with a panic guard: a panic here is not
// caught by controller-runtime's own RecoverPanic, which wraps reconcilers
// only, so an unguarded panic would crash the whole engine process on the
// reaper's own schedule.
func (r *Reaper) tick(ctx context.Context, log *slog.Logger) {
	defer func() {
		if rec := recover(); rec != nil {
			log.ErrorContext(ctx, "torrent: orphan reap panicked", "panic", rec)
		}
	}()
	if err := r.ReapOnce(ctx); err != nil {
		log.ErrorContext(ctx, "torrent: orphan reap failed", "error", err)
	}
}

// ReapOnce runs one list-against-list pass. It is exported so a test can
// call it directly, bypassing [Reaper.Start]'s ticker and the ordinary
// Reconcile/delete path entirely -- deleting a Download and letting
// [Reconciler.reconcileDeleting] run would not exercise this code at all.
func (r *Reaper) ReapOnce(ctx context.Context) error {
	if r.Engine != nil && !r.Engine.Ready() {
		return nil
	}

	ctx, span := tracing.Start(ctx, "torrent.ReapOnce")
	defer span.End()
	log := logging.FromContext(ctx).With("engine", r.EngineID)

	items, err := r.Engine.Client.List(ctx)
	if err != nil {
		return fmt.Errorf("torrent: list client transfers: %w", err)
	}
	if len(items) == 0 {
		r.forgetAll()
		return nil
	}

	var dls downloadv1alpha1.DownloadList
	if err := r.Client.List(ctx, &dls, client.MatchingLabels{downloadv1alpha1.LabelEngine: r.EngineID}); err != nil {
		return fmt.Errorf("torrent: list downloads for %s: %w", r.EngineID, err)
	}
	known := make(map[string]struct{}, len(dls.Items))
	for i := range dls.Items {
		if id := dls.Items[i].Status.DownloadID; id != "" {
			known[id] = struct{}{}
		}
	}

	for _, id := range r.orphansDue(items, known) {
		if err := r.Engine.Client.Remove(ctx, id, false); err != nil && !errors.Is(err, download.ErrNotFound) {
			log.ErrorContext(ctx, "torrent: orphan reap: remove failed", "id", id, "error", err)
			continue
		}
		r.forget(id)
		log.InfoContext(ctx, "torrent: reaped orphaned transfer with no matching Download", "id", id)
	}
	return nil
}

// orphansDue updates the unmatched-since bookkeeping from one List pass and
// returns the ids that have now been unmatched continuously for at least
// [Reaper.orphanGrace] -- see the type doc's "Guard two".
func (r *Reaper) orphansDue(items []download.Item, known map[string]struct{}) []string {
	now := r.now()
	grace := r.orphanGrace()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unmatchedSince == nil {
		r.unmatchedSince = make(map[string]time.Time)
	}

	seen := make(map[string]struct{}, len(items))
	var due []string
	for _, item := range items {
		seen[item.ID] = struct{}{}
		if _, ok := known[item.ID]; ok {
			// Matched: this transfer belongs to a Download this replica's
			// cache currently holds. Forget any earlier unmatched sighting
			// so a transient miss (a delayed status write, a re-Get racing
			// this pass) does not carry a stale clock forward.
			delete(r.unmatchedSince, item.ID)
			continue
		}
		first, tracked := r.unmatchedSince[item.ID]
		if !tracked {
			r.unmatchedSince[item.ID] = now
			continue
		}
		if now.Sub(first) >= grace {
			due = append(due, item.ID)
		}
	}

	// Drop bookkeeping for ids the client no longer reports at all (already
	// removed some other way) so the map does not grow unbounded across a
	// long-lived engine process.
	for id := range r.unmatchedSince {
		if _, ok := seen[id]; !ok {
			delete(r.unmatchedSince, id)
		}
	}
	return due
}

func (r *Reaper) forget(id string) {
	r.mu.Lock()
	delete(r.unmatchedSince, id)
	r.mu.Unlock()
}

func (r *Reaper) forgetAll() {
	r.mu.Lock()
	r.unmatchedSince = nil
	r.mu.Unlock()
}
