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

package usenet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/engine"
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

	// DefaultOrphanGrace is how old a transfer with no matching Download
	// must be -- by its own [download.Item.AddedAt], see
	// grabarr/engine.OrphanClock -- before [Reaper] treats it as an
	// orphan rather than one this replica added moments ago and has not yet
	// matched.
	//
	// It must comfortably exceed the slowest path from Add to a matched
	// Download: [Reconciler.getOrAdd] resolves the payload under
	// [DefaultResolveTimeout] (60s), then Adds and the caller applies
	// telemetry that sets status.downloadID. Ten minutes is a wide margin
	// over that, matching grabarr/engine/torrent's identical constant and
	// its reasoning against that package's own 5-minute reconcile timeout.
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

// Reaper is the backstop behind the engine finalizer (grabarr/engine's
// [engine.Finalizer], gap-fix ruling R-6). The finalizer makes the ordinary
// path safe: a deleted Download is not gone until this engine has removed
// its transfer. But the Download controller drops that finalizer on the
// engine's behalf once the engine has been gone for a bounded timeout --
// that is what keeps a deletion from wedging forever on a DownloadClient
// that no longer exists -- and a transfer this engine still holds when it
// comes back then has no Download at all: a usenet fetch goes on spending
// the provider's connection budget with nothing in any CR to say so.
//
// It is deliberately level-driven rather than triggered by the delete event
// that [Reconciler.reconcileDeleting] handles: on a timer, it lists every
// transfer [Reconciler.Download] currently holds and every Download this
// replica's cache currently holds labelled for [Reaper.Engine], and removes
// any transfer with no matching Download. Reconciling list against list
// recovers from all three cases above because it never depends on having
// observed the event that caused the loss in the first place -- unlike an
// edge-triggered handshake, which fails exactly those cases.
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
// is reaped only once it is at least [Reaper.OrphanGrace] old -- see
// [DefaultOrphanGrace] for why that duration is sized against this
// package's own slowest Add-to-match path. This catches the case cache-sync
// alone cannot: a fully synced cache that simply has not yet been told about
// a Download this same replica added moments ago. The age is the
// transfer's own [download.Item.AddedAt], which the client persists in its
// scratch manifest, so an engine restart does not grant an orphan a fresh
// grace period ([engine.OrphanClock] has the details, and the fallback for
// a transfer whose age is unknown).
//
// [Reconciler] itself needs no equivalent re-attach gate here: unlike
// grabarr/engine/torrent's [Engine], [download.Client] as built by
// [BuildClient] has already re-attached synchronously by the time it
// exists at all (pkg/download/usenet.New's own doc comment), so there is no
// window where a Reaper could be constructed before re-attach completes.
//
// # deleteData is always false
//
// By the time a transfer is old enough to be reaped, its Download is gone
// from the apiserver entirely -- there is no spec.removeDataOnDelete left to
// read, and the Download controller's finalizer already ran
// fsops.SafeRemove against status.outputPath when spec asked for it (it
// does so once the engine finalizer is gone, or once it stopped waiting
// for an engine that was gone -- the only way a transfer ends up here). Passing deleteData=true here
// would ask the client to delete files the controller either already
// removed (redundant, and this package cannot know whether the client
// treats a missing file as success) or deliberately left in place (a
// straight violation of removeDataOnDelete=false, which this reaper has no
// way to re-derive once the object is gone). The reaper's job is narrower
// than the finalizer's: stop the client from holding the transfer's
// connection budget open, not manage disk state a controller already
// decided.
//
// # Field manager
//
// Reaper writes nothing to Download.status and touches no
// metadata.finalizers; it only calls [download.Client.Remove]. It shares no
// state with [Reconciler] beyond the embedded [download.Client], and does
// not require Reconcile to have run for a given Download at all.
type Reaper struct {
	// Client lists Downloads. In production this is the manager's
	// cache-backed client (the same one [Reconciler.Client] is), so
	// [Reaper.Cache] should be the same manager's cache.
	Client client.Client

	// Download is the embedded client this replica manages -- the same one
	// [Reconciler.Download] holds, already re-attached by the time it was
	// built (see the type doc).
	Download download.Client

	// Engine is this replica's identity, "<client>-0" -- the same value
	// [Reconciler.Engine] holds and the same label value Downloads for this
	// replica carry.
	Engine string

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

	clock engine.OrphanClock
}

// NeedLeaderElection makes the reaper run on every replica rather than only
// the leader. Usenet clients are capped at one replica by DownloadClientSpec's
// own CEL rule, so this is belt and braces today, but it matches
// grabarr/engine/torrent's identical reasoning and pkg/k8s.EveryReplica's:
// each engine replica embeds its own client and knows only its own
// transfers, so a leader-elected singleton is the wrong shape even where it
// would currently be harmless.
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
// WaitForCacheSync, reaps once straight away -- so an engine that restarts
// more often than [Reaper.ReapInterval] still reaps orphans older than the
// grace -- then reaps on [Reaper.ReapInterval] until ctx is done. It returns nil on cancellation, matching every other
// Runnable in this tree (e.g. catalogarr/controller/wantedcron): a Runnable
// that returns an error takes the whole manager down with it, and a
// graceful shutdown is not an error.
func (r *Reaper) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("runnable", "usenet-reaper", "engine", r.Engine)

	if r.Cache != nil && !r.Cache.WaitForCacheSync(ctx) {
		// Only reachable when ctx is already done (manager shutting down);
		// returning an error here would fail the whole manager during a
		// graceful stop, same reasoning as pkg/k8s.CacheSyncChecker.
		return nil
	}

	ticker := time.NewTicker(r.reapInterval())
	defer ticker.Stop()
	log.InfoContext(ctx, "usenet: orphan reaper started", "interval", r.reapInterval(), "grace", r.orphanGrace())
	r.tick(ctx, log)
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
			log.ErrorContext(ctx, "usenet: orphan reap panicked", "panic", rec)
		}
	}()
	if err := r.ReapOnce(ctx); err != nil {
		log.ErrorContext(ctx, "usenet: orphan reap failed", "error", err)
	}
}

// ReapOnce runs one list-against-list pass. It is exported so a test can
// call it directly, bypassing [Reaper.Start]'s ticker and the ordinary
// Reconcile/delete path entirely -- deleting a Download and letting
// [Reconciler.reconcileDeleting] run would not exercise this code at all.
func (r *Reaper) ReapOnce(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "usenetengine.ReapOnce")
	defer span.End()
	log := logging.FromContext(ctx).With("engine", r.Engine)

	items, err := r.Download.List(ctx)
	if err != nil {
		return fmt.Errorf("usenetengine: list client transfers: %w", err)
	}
	if len(items) == 0 {
		r.clock.Reset()
		return nil
	}

	var dls downloadv1alpha1.DownloadList
	if err := r.Client.List(ctx, &dls, client.MatchingLabels{downloadv1alpha1.LabelEngine: r.Engine}); err != nil {
		return fmt.Errorf("usenetengine: list downloads for %s: %w", r.Engine, err)
	}
	known := make(map[string]struct{}, len(dls.Items))
	for i := range dls.Items {
		if id := dls.Items[i].Status.DownloadID; id != "" {
			known[id] = struct{}{}
		}
	}

	for _, id := range r.orphansDue(items, known) {
		if err := r.Download.Remove(ctx, id, false); err != nil && !errors.Is(err, download.ErrNotFound) {
			log.ErrorContext(ctx, "usenet: orphan reap: remove failed", "id", id, "error", err)
			continue
		}
		r.clock.Forget(id)
		log.InfoContext(ctx, "usenet: reaped orphaned transfer with no matching Download", "id", id)
	}
	return nil
}

// orphansDue records one List pass against the known ids and returns the
// transfers now old enough to reap -- see the type doc's "Guard two".
func (r *Reaper) orphansDue(items []download.Item, known map[string]struct{}) []string {
	return r.clock.Due(items, known, r.now(), r.orphanGrace())
}
