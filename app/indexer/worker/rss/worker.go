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

package rss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/blocklist"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/app/indexer/rssschedule"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

const (
	// HeartbeatInterval is how often the poll extends its ack deadline.
	// ConsumerIndexRSS's AckWait is 60s, pinned to the pod's
	// terminationGracePeriodSeconds; the topology's own rule is that work
	// which can outlast the grace period heartbeats rather than raising
	// AckWait past it. It is at most a third of the durable's
	// first-delivery deadline, events.AckDeadline(sub, 1), so two
	// heartbeats can be lost before a lapse (NATS research 2026-10-07, S9;
	// held by test/guards.TestHeartbeatsFitTheirDeadline).
	//
	// This is NOT Subscription.Heartbeat, which is the broker's idle
	// heartbeat for connection liveness and extends nothing.
	HeartbeatInterval = 20 * time.Second

	// maxPages bounds one poll. An indexer that keeps returning full pages
	// would otherwise hold the delivery open indefinitely; the next poll
	// picks up where this one stopped, and RSS only needs the newest rows.
	maxPages = 4

	// pageSize is per Torznab request. Prowlarr's RSS default.
	pageSize = 100

	// retryAfterShutdown is how long a poll cut short by SIGTERM waits
	// before the next pod picks it up.
	retryAfterShutdown = 30 * time.Second

	// statusRetry is how long to wait after a failed status apply. The write
	// is against the apiserver, so a failure there is a blip or a conflict,
	// and the whole poll is idempotent on redelivery.
	statusRetry = 10 * time.Second

	// readRetry is how long to wait after a failed Indexer read.
	readRetry = 5 * time.Second

	// minFailureRetry is the floor for a redelivery after a failed poll. The
	// ladder's first step is a zero period (Prowlarr does not disable on the
	// first failure), and hammering the indexer immediately is exactly what
	// the ladder exists to prevent.
	minFailureRetry = time.Minute

	// maxFailureRetry caps the redelivery delay. Past this the SCHEDULED
	// task, not the redelivery, is what keeps the indexer's cadence, and a
	// delivery held open for hours only burns one of MaxDeliver's four
	// attempts.
	maxFailureRetry = 15 * time.Minute
)

// Searcher is one indexer's search call. The Indexer reconciler supplies the
// concrete *torznab.Client, already built with this host's single injected
// rate limiter; this package never constructs one.
//
// It is a local interface seam rather than a shared symbol so that the search
// fan-out and this worker do not co-own a type neither of them defines.
type Searcher interface {
	Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error)
}

// Deps is everything the worker needs from the process around it.
type Deps struct {
	// Client reads Indexer objects and writes their status.
	Client client.Client

	// Reader is an uncached reader -- manager.GetAPIReader() -- for the
	// compare-and-swap status write at the end of a poll: a read from the
	// cache lags the write it raced and would conflict again on every
	// attempt. nil falls back to Client.
	Reader client.Reader

	// Bus carries the release firehose and the next scheduled poll, and the
	// clustarr-indexer-limits query ring every page reserves on before it
	// is requested -- the ring the search fan-out reserves on, so
	// spec.limits.queryLimit is the indexer's whole traffic, as Prowlarr
	// counts IndexerQuery and IndexerRss together.
	Bus events.Bus

	// Index is the local release index. Its Upsert is the source of
	// status.lastRssNewCount.
	Index relindex.Store

	// SearcherFor returns the search client for one indexer.
	SearcherFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (Searcher, error)

	// Clock is a seam for tests; nil means time.Now.
	Clock func() time.Time
}

// Worker is the indexarr-rss consumer: one message polls one indexer.
type Worker struct {
	Deps Deps
}

// NewWorker returns a Worker over d.
func NewWorker(d Deps) *Worker { return &Worker{Deps: d} }

// errQueryLimit is a poll whose first page the query ring refused: the
// indexer is at spec.limits.queryLimit and was not asked at all.
type errQueryLimit struct{ retryAt time.Time }

func (e *errQueryLimit) Error() string {
	return "rss: query limit reached; next room at " + e.retryAt.UTC().Format(time.RFC3339)
}

func (w *Worker) reader() client.Reader {
	if w.Deps.Reader != nil {
		return w.Deps.Reader
	}
	return w.Deps.Client
}

func (w *Worker) now() time.Time {
	if w.Deps.Clock != nil {
		return w.Deps.Clock()
	}
	return time.Now()
}

// Subscription is the indexarr-rss durable consumer. It reads the tuning from
// the topology rather than restating it, exactly as
// rssmatcher.Handler.Subscription does, so AckWait and Heartbeat live in one
// place.
func (w *Worker) Subscription() events.Subscription {
	spec, ok := events.Default().Consumer(events.ConsumerIndexRSS)
	if !ok {
		// Unreachable: ConsumerIndexRSS is in defaultConsumers(). A zero
		// Subscription fails Validate loudly at Subscribe time rather than
		// silently consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager registers the subscription as a manager.Runnable so it
// starts with the manager and drains on shutdown.
//
// It is a k8s.EveryReplica rather than a manager.RunnableFunc: the queue
// workers run on every replica, and a bare RunnableFunc has no
// NeedLeaderElection method, so controller-runtime would put it behind the
// leader lease.
func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, w.Subscription(), w.Handle)
		if err != nil {
			return fmt.Errorf("indexarr: subscribe rss: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle polls ONE indexer -- the one named in the task -- and never lists
// Indexers. One message is one indexer, which is what keeps a broken
// indexer's failure from reaching another indexer's delivery.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	ctx, span := tracing.Start(ctx, "rss.Worker.Handle")
	defer span.End()

	env := m.Envelope()
	if env == nil {
		return events.Discard("rss task has no envelope", errors.New("rss: nil envelope"))
	}
	var task schema.RssTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable rss task", err)
	}
	if task.IndexerRef.Name == "" || task.IndexerRef.Namespace == "" {
		// Both halves are required: the envelope key this poll will publish
		// under is namespace + "/" + name, and the matcher Discards a key it
		// cannot Cut. Refuse here, where it is one message, rather than
		// there, where it is every release from this indexer.
		return events.Discard("rss task is missing a namespaced indexer reference",
			fmt.Errorf("rss: indexerRef=%q", task.IndexerRef.String()))
	}
	ctx = logging.With(ctx, "indexer", task.IndexerRef.Name, "namespace", task.IndexerRef.Namespace)
	log := logging.FromContext(ctx)

	var idx indexv1alpha1.Indexer
	key := client.ObjectKey{Namespace: task.IndexerRef.Namespace, Name: task.IndexerRef.Name}
	if err := w.Deps.Client.Get(ctx, key, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			// The Indexer was deleted between the schedule and this
			// delivery. Nothing to poll and nothing to reschedule.
			log.Debug("rss: indexer no longer exists")
			return nil
		}
		return events.Retry(readRetry, err)
	}

	now := w.now()

	// The operator's switches. Acknowledge without polling and WITHOUT a
	// status write: nothing observed, nothing to record. No reschedule
	// either -- re-enabling bumps the generation, the reconciler reconciles
	// and seeds a fresh schedule.
	if !ptr.Deref(idx.Spec.Enabled, true) || !ptr.Deref(idx.Spec.EnableRss, true) {
		log.Debug("rss: indexer does not poll", "enabled", ptr.Deref(idx.Spec.Enabled, true),
			"enableRss", ptr.Deref(idx.Spec.EnableRss, true))
		return nil
	}

	// Inside the escalation ladder's backoff window. Do not query -- that is
	// the whole point of the window -- but keep the cadence by scheduling
	// the poll that lands when the window expires.
	if !idxstatus.Healthy(idx.Status, now) {
		at := idx.Status.DisabledUntil.Time
		log.Debug("rss: indexer is backing off; not querying", "until", at)
		if err := rssschedule.ScheduleNext(ctx, w.Deps.Bus, &idx, at); err != nil {
			return events.Retry(statusRetry, err)
		}
		return nil
	}

	fetched, queries, pollErr := w.pollOnce(ctx, m, &idx, task, now)

	if pollErr != nil && (errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded)) {
		// The pod is going away mid-poll. This is our shutdown, not the
		// indexer's fault: recording a failure here would escalate every
		// indexer in the namespace on every rollout, and the ladder would
		// then disable them for minutes. Nak and let the next pod take it;
		// nothing has been indexed or published, so redelivery is a clean
		// retry.
		return events.Retry(retryAfterShutdown, pollErr)
	}

	var limited *errQueryLimit
	if errors.As(pollErr, &limited) {
		// At the limit before the first page. Nothing was asked and nothing
		// observed, so nothing is recorded -- a budget is not a failure, and
		// lastRssAt must keep saying when the feed was last READ. The chain
		// is held open at the poll's own cadence, or at the moment the
		// window next has room if that is later; a poll sooner would only be
		// refused again.
		at := now.Add(rssschedule.Interval(&idx))
		if limited.retryAt.After(at) {
			at = limited.retryAt
		}
		log.Info("rss: query limit reached; not polling", "next", at)
		if err := rssschedule.ScheduleNext(ctx, w.Deps.Bus, &idx, at); err != nil {
			return events.Retry(statusRetry, err)
		}
		return nil
	}

	var (
		inserted int
		esc      idxstatus.Escalation
	)
	if pollErr == nil {
		var published, dropped int
		var err error
		inserted, published, dropped, err = w.indexAndPublish(ctx, &idx, fetched, now)
		if err != nil {
			return events.Retry(statusRetry, err)
		}
		// dropped counts rows the INDEX refused. Some of them are still
		// published -- see indexAndPublish -- so dropped and published are
		// not complements.
		log.Info("rss: poll complete", "fetched", len(fetched),
			"inserted", inserted, "published", published, "dropped", dropped)
	}

	// ONE apply, whatever happened, and it goes through app/indexer/status so
	// the indexarr-worker owned set is declared in exactly one place. Never
	// an early return with a partial status: the early return is usually the
	// transient case, which is exactly when a healthy object would be gutted
	// by a blip.
	//
	// And a compare-and-swap. idx was read before the poll, and a poll is up
	// to maxPages requests of spec.timeout each -- minutes -- while the
	// search fan-out writes under this same manager from every replica. An
	// apply seeded from any read but the latest rolls back what landed in
	// between (a lost update, which no release test can see); PatchCAS seeds
	// from a fresh read, applies with its resourceVersion, and on a Conflict
	// redoes the ladder step and the indexedReleases increment from a new
	// one.
	prev, _, err := idxstatus.PatchCAS(ctx, w.reader(), w.Deps.Client, k8s.ManagerIndexWorker, key,
		func(fresh *indexv1alpha1.Indexer, ac *indexac.IndexerStatusApplyConfiguration) bool {
			if pollErr != nil {
				esc = idxstatus.RecordFailure(fresh.Status, now, pollErr.Error())
			} else {
				esc = idxstatus.RecordSuccess(fresh.Status, now)
				ac.WithLastRssAt(metav1.NewTime(now)).
					WithLastRssNewCount(int32(inserted)). //nolint:gosec // bounded by maxPages*pageSize
					WithIndexedReleases(fresh.Status.IndexedReleases + int64(inserted))
			}
			idxstatus.ApplyEscalation(ac, esc, fresh.Status)
			return false
		})
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug("rss: indexer was deleted during the poll")
			return nil
		}
		return events.Retry(statusRetry, err)
	}
	// indexer.disabled|recovered|limited, after the apply landed and measured
	// from the read the apply was seeded from.
	idxstatus.PublishTransitions(ctx, w.Deps.Bus, prev, idxstatus.Transition{
		Prev: prev.Status, Escalation: &esc, Failed: pollErr != nil, Queries: queries, At: now,
	})

	// Reschedule BEFORE returning the poll error, so a failing indexer keeps
	// its cadence and recovers on its own rather than waiting for the next
	// reconcile.
	if err := rssschedule.ScheduleNext(ctx, w.Deps.Bus, prev, now.Add(rssschedule.Interval(prev))); err != nil {
		return events.Retry(statusRetry, err)
	}
	if pollErr != nil {
		return events.Retry(retryAfterFailure(esc, now), pollErr)
	}
	return nil
}

// retryAfterFailure derives the redelivery delay from the escalation the
// failure produced, so the redelivery lands when the ladder next permits a
// query rather than on a schedule that knows nothing about it.
func retryAfterFailure(esc idxstatus.Escalation, now time.Time) time.Duration {
	d := minFailureRetry
	if esc.DisabledUntil != nil {
		d = esc.DisabledUntil.Sub(now)
	}
	return min(max(d, minFailureRetry), maxFailureRetry)
}

// pollOnce reads up to maxPages of the indexer's newest rows, heartbeating so
// a slow feed does not outlive AckWait.
//
// The heartbeat is INSIDE the paging loop, at the top of each iteration, not
// around the whole call: one Search is one HTTP request bounded by
// spec.timeout (default 30s), so the only way a poll outlives a 60s AckWait
// is by making several of them.
//
// Each page reserves one query on the indexer's query ring BEFORE its
// request, as the search fan-out does: a request that fails or times out
// still reached the indexer and still spends its budget, and Prowlarr counts
// every IndexerRss request against QueryLimit. A page the ring refuses is not
// requested. The first page refused is an *errQueryLimit -- the poll asked
// nothing -- and a later one ends the paging with what was read, as a short
// page does.
//
// queries is the window count when a page's reservation filled the window,
// for the limited event, nil otherwise.
func (w *Worker) pollOnce(
	ctx context.Context,
	m events.Message,
	idx *indexv1alpha1.Indexer,
	task schema.RssTask,
	now time.Time,
) (fetched []torznab.Release, queries *int32, err error) {
	if w.Deps.SearcherFor == nil {
		return nil, nil, errors.New("rss: no SearcherFor was wired")
	}
	s, err := w.Deps.SearcherFor(ctx, idx)
	if err != nil {
		// No request reached the indexer, so no query is counted.
		return nil, nil, fmt.Errorf("rss: build search client: %w", err)
	}

	cats := categoryIDsFor(idx, task)
	since := pollSince(idx, task)

	var (
		all  []torznab.Release
		last time.Time
	)
	start := now
	for page := range maxPages {
		if ctx.Err() != nil {
			return all, queries, ctx.Err()
		}
		if beat := w.now(); last.IsZero() || beat.Sub(last) >= HeartbeatInterval {
			last = beat
			if err := m.InProgress(ctx); err != nil {
				return all, queries, fmt.Errorf("rss: heartbeat: %w", err)
			}
		}
		r, ok := w.reserveQuery(ctx, idx)
		if !ok {
			if page == 0 {
				return nil, nil, &errQueryLimit{retryAt: r.RetryAt}
			}
			break
		}
		if r.Crossed(limits.QueryLimit(idx)) {
			queries = &r.Count
		}
		// t=search with an empty q is the RSS call: the indexer's newest
		// rows, unfiltered.
		batch, err := s.Search(ctx, torznab.Query{
			Type:       torznab.ModeSearch,
			Categories: cats,
			Limit:      pageSize,
			Offset:     page * pageSize,
		})
		w.recordQuery(idx.Name, start, err)
		start = w.now()
		if err != nil {
			return all, queries, err
		}
		// One page is one query, which is the unit this histogram documents.
		metrics.IndexerReleasesReturned.WithLabelValues(idx.Name).Observe(float64(len(batch)))

		all = append(all, batch...)
		if len(batch) < pageSize || reachedSince(batch, since) {
			// RssTask.Since exists so the worker can stop paging early.
			break
		}
	}
	return all, queries, nil
}

// reserveQuery reserves one page request on idx's query ring, reporting
// whether the page may be requested. An accounting outage fails OPEN,
// logged: it must not turn a feed the indexer would have served into a
// failure the escalation ladder would punish. No bus means no accounting (a
// unit test).
func (w *Worker) reserveQuery(ctx context.Context, idx *indexv1alpha1.Indexer) (limits.Reservation, bool) {
	if w.Deps.Bus == nil {
		return limits.Reservation{Allowed: true}, true
	}
	r, err := limits.ReserveQuery(ctx, w.Deps.Bus.KV(events.BucketIndexerLimits), idx, w.now())
	if err != nil {
		logging.FromContext(ctx).Warn("rss: query accounting failed; polling anyway", "err", err)
		return limits.Reservation{Allowed: true}, true
	}
	return r, r.Allowed
}

// recordQuery emits the two per-query metrics. Both are labelled by the
// Indexer's object name, which is bounded by the number of Indexer objects;
// nothing here is ever labelled by a release title or a feed URL.
func (w *Worker) recordQuery(indexerName string, start time.Time, err error) {
	metrics.IndexerQueryDuration.WithLabelValues(indexerName, "rss").
		Observe(w.now().Sub(start).Seconds())
	metrics.IndexerQueriesTotal.WithLabelValues(indexerName, queryOutcome(err)).Inc()
}

// queryOutcome names the failure so a 429 reads as pacing rather than as a
// generic error.
func queryOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	var te *torznab.Error
	if errors.As(err, &te) {
		switch te.HTTPStatus {
		case 429:
			return "rate_limited"
		case 410:
			return "banned"
		}
	}
	return "error"
}

// indexAndPublish offers every fetched row to the index and then publishes
// every projected release to the firehose.
//
// lastRssNewCount comes from the index's inserted count, never from
// len(fetched) and never from the publish count. There are three different
// numbers here and they routinely differ: the feed returns the same 100 rows
// every 15 minutes on a quiet indexer, UNIQUE(indexer, guid) decides how many
// of them are new, and the bus suppresses whatever its 2h dedup window
// already holds.
func (w *Worker) indexAndPublish(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	fetched []torznab.Release,
	now time.Time,
) (inserted, published, dropped int, err error) {
	log := logging.FromContext(ctx)
	protocol := idx.Status.Protocol
	if protocol == "" && idx.Spec.Generic != nil {
		// A generic upstream declares its protocol in the spec, so a poll
		// that beats the first reconcile still ships a usable release rather
		// than one the consumer cannot route.
		protocol = idx.Spec.Generic.Protocol
	}

	rows := make([]relindex.Release, 0, len(fetched))
	projected := make([]schema.Release, 0, len(fetched))
	belowSeeders := 0
	for _, r := range fetched {
		// spec.minimumSeeders, as Sonarr's TorrentSeedingSpecification: a
		// torrent the indexer reports below it is neither indexed nor
		// published, so no consumer of the firehose ever grabs it.
		if BelowMinimumSeeders(idx, string(protocol), r.Seeders) {
			belowSeeders++
			continue
		}
		rel := ProjectRelease(r, idx.Name, string(protocol))
		rel.FetchedAt = now

		row, rowErr := indexRow(rel, idx.Name, now)
		if rowErr != nil {
			return 0, 0, 0, rowErr
		}
		// ONE hostile row must not take the page down with it. Upsert
		// validates the whole batch before it opens its transaction and
		// fails all of it, and this poll would then return before both the
		// status write and ScheduleNext -- so nothing publishes, no failure
		// is recorded (the Indexer reads perfectly healthy), and after
		// MaxDeliver the delivery is dead-lettered with no pending schedule
		// left. The junk row is still in the feed next time and nothing
		// re-seeds the chain. That is permanent, silent, and reachable from
		// upstream XML: TitleNorm("???") is "" -- a title of punctuation and
		// symbols alone has no letter or digit in any script -- and
		// pkg/torznab does not backfill an absent GUID.
		if reason := rejectReason(row); reason != "" {
			dropped++
			metrics.IndexerReleasesDropped.WithLabelValues(idx.Name).Inc()

			// An unsearchable title is a reason the INDEX refuses the row,
			// not a reason the matcher cannot use it. ProjectRelease copies
			// the indexer's own attrs into Info.IDs before it parses and
			// keeps them even when the parse fails, and rssmatcher matches
			// movies and series on tmdb/tvdb ids BEFORE it ever looks at a
			// title -- so a release whose title is only punctuation but
			// which carries an imdbid attr is a perfectly matchable release
			// that merely cannot be stored.
			//
			// Publishing it is safe precisely because the GUID is valid
			// here: Nats-Msg-Id is sha1(indexer:guid), so it is distinct and
			// the 2h dedup window behaves normally. That is exactly what is
			// NOT true of the empty-GUID case, where every such row would
			// hash identically and the window would collapse them all into
			// one message -- so those stay dropped from both.
			if reason == reasonUnsearchableTitle && len(rel.Info.IDs) > 0 {
				log.Warn("rss: the index cannot store this release; publishing it on its ids alone",
					"reason", reason, "guid", rel.Info.GUID, "title", clip(rel.Info.Title))
				projected = append(projected, rel)
				continue
			}
			log.Warn("rss: dropping a release the index cannot store",
				"reason", reason, "guid", rel.Info.GUID, "title", clip(rel.Info.Title))
			continue
		}
		projected = append(projected, rel)
		rows = append(rows, row)
	}

	if belowSeeders > 0 {
		log.Debug("rss: skipped releases below spec.minimumSeeders",
			"skipped", belowSeeders, "minimumSeeders", idx.Spec.MinimumSeedersOrDefault())
	}

	// Upsert FIRST, and take lastRssNewCount from its truthful `inserted`.
	// Every fetched row is offered; the store decides what is new. Counting
	// len(fetched) instead would report 100 new releases every 15 minutes on
	// a feed that has not moved.
	if w.Deps.Index != nil && len(rows) > 0 {
		inserted, err = w.Deps.Index.Upsert(ctx, rows)
		if err != nil {
			// Deliberately still fatal for this delivery. rejectReason
			// filters everything the store rejects today, so reaching here
			// means either a real storage failure -- a locked file, a full
			// disk, worth a retry -- or a validation rule that rejectReason
			// has drifted away from, which must be loud rather than
			// swallowed. TestRejectReasonAgreesWithTheRealStore zeroes
			// every field of relindex.Release in turn against a real
			// store, so it catches any new rule that rejects a
			// zero-valued field -- which is how all five of today's
			// rules work.
			return 0, 0, dropped, fmt.Errorf("rss: index %d releases: %w", len(rows), err)
		}
	}

	// Then publish ALL of them, not just the new ones. Upsert returns a
	// count, not a set, and the firehose is idempotent by design:
	// Nats-Msg-Id is sha1(indexer:guid) and CLUSTARR_RELEASES dedups for 2h,
	// so re-reading the same RSS page republishes nothing.
	//
	// The two windows are deliberately different and the asymmetry is
	// correct: the index keeps 72h, the dedup window is 2h. A release last
	// seen three hours ago is still in the index (inserted == 0) but its
	// dedup entry has expired, so it is republished and the matcher
	// re-evaluates it once. That costs one evaluation and can only help --
	// the item's monitored state or quality profile may have changed since.
	// What it must never do is inflate lastRssNewCount, which is why that
	// number comes from `inserted` and not from `published`.
	if len(projected) > 0 {
		// Each firehose release carries every scope that blocks it
		// (ADR-0019 §6.14), almost always none: the firehose does not know
		// the item, so the RSS matcher keeps those naming its match or the
		// global scope. A failure publishes them unmarked; the manager
		// re-checks its own tombstones.
		if berr := blocklist.AllBlocks(ctx, w.Deps.Index, projected, time.Now()); berr != nil {
			log.Warn("rss: reading block state", "err", berr)
		}
		published, err = PublishReleases(ctx, w.Deps.Bus, idx.Namespace, idx.Name, projected)
		if err != nil {
			return inserted, published, dropped, err
		}
	}
	return inserted, published, dropped, nil
}

// maxLoggedTitle caps a release title in a log line. The title is untrusted
// upstream XML and nothing else bounds it.
const maxLoggedTitle = 200

// clip shortens s for a log line, backing up to a rune boundary so a
// multi-byte sequence is never cut in half.
func clip(s string) string {
	if len(s) <= maxLoggedTitle {
		return s
	}
	n := maxLoggedTitle
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// reasonUnsearchableTitle is the one rejection a release can still be
// published in spite of. It is a named constant rather than a literal
// because indexAndPublish compares against it, and a reworded string would
// otherwise silently stop that comparison matching.
const reasonUnsearchableTitle = "the title normalises to nothing, so the row would be unsearchable"

// rejectReason names why relindex.Upsert would refuse row, or "" when it
// would accept it.
//
// It mirrors relindex's own validate (pkg/relindex/upsert.go) rather than
// re-deriving the rules, and mirroring is normally how a guard drifts --
// so the mirror is held to the real thing by a contract test against a real
// sqlite store (TestRejectReasonAgreesWithTheRealStore), not by a comment.
//
// Only the first two are reachable from a feed. indexRow always sets Indexer
// to the object name and FetchedAt to the poll clock, and only ever sets
// PublishedAt from a non-nil, non-zero value -- but they are mirrored anyway,
// because "structurally impossible today" is a property of indexRow, and the
// batch-kill this guards is too expensive to leave resting on that.
func rejectReason(row relindex.Release) string {
	switch {
	case row.Indexer == "":
		return "the indexer name is empty"
	case row.GUID == "":
		return "the indexer reported no guid"
	case row.TitleNorm == "":
		// release.TitleNorm keeps letters, digits and marks in every script,
		// so only a title made of punctuation and symbols alone normalises
		// to nothing. The row would be invisible to every text search while
		// still counting in Stats, which is why the store refuses it.
		//
		// It is the one reason indexAndPublish treats as index-only: the
		// release can still match on its ids.
		return reasonUnsearchableTitle
	case row.FetchedAt.IsZero():
		return "fetchedAt is the zero time"
	case row.PublishedAt != nil && row.PublishedAt.IsZero():
		return "publishedAt points at the zero time; absence must be nil"
	}
	return ""
}

// indexRow renders one projected release as an index row. InfoJSON is the
// whole schema.Release, so a replay needs no re-query.
func indexRow(rel schema.Release, indexerName string, now time.Time) (relindex.Release, error) {
	raw, err := json.Marshal(rel)
	if err != nil {
		return relindex.Release{}, fmt.Errorf("rss: encode index row for %s/%s: %w",
			indexerName, rel.Info.GUID, err)
	}
	// TitleNorm is release.TitleNorm, NOT release.Normalize. relindex
	// stores what it is given and escapes Query.Text without normalising it,
	// so the indexed column and the query must go through ONE function or
	// the index answers nothing -- and pkg/relindex cannot catch a mismatch
	// from the inside. release.Normalize's own doc says it preserves case and
	// is "for display / re-embedding ... not for equality comparison".
	// TitleNorm is CleanTitle's Unicode-aware sibling: identical on printable
	// ASCII, so rows written under CleanTitle stay findable, but a Cyrillic
	// or CJK title keeps its own tokens instead of normalising to its ASCII
	// residue. app/indexer/search and app/indexer/query use the same function.
	row := relindex.Release{
		Indexer:    indexerName,
		GUID:       rel.Info.GUID,
		Title:      rel.Info.Title,
		TitleNorm:  release.TitleNorm(rel.Info.Title),
		Group:      rel.Info.ReleaseGroup,
		Protocol:   string(rel.Info.Protocol),
		Categories: narrow(rel.Info.Categories),
		SizeBytes:  rel.Info.SizeBytes,
		FetchedAt:  now,
		InfoJSON:   raw,
	}
	// The same nil-preserving pointer the firehose carries. relindex.Upsert
	// rejects a non-nil pointer to the zero time outright, so absence must
	// stay absence rather than become a zero date.
	if p := rel.Info.PublishedAt; p != nil && !p.Time.IsZero() {
		row.PublishedAt = new(p.Time)
	}
	return row, nil
}

// narrow converts the wire's []int32 categories to the index's []int.
func narrow(in []int32) []int {
	if len(in) == 0 {
		return nil
	}
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}

// categoryIDsFor picks the categories one poll queries: the task's, when the
// scheduler pinned some, otherwise the Indexer's own.
func categoryIDsFor(idx *indexv1alpha1.Indexer, task schema.RssTask) []newznab.CategoryID {
	ids := task.Categories
	if len(ids) == 0 {
		ids = idx.Spec.Categories
	}
	if len(ids) == 0 {
		return nil
	}
	out := make([]newznab.CategoryID, len(ids))
	for i, id := range ids {
		out[i] = newznab.CategoryID(id)
	}
	return out
}

// pollSince is the publish time past which paging may stop.
//
// It is the LATER of the task's own bound and when this indexer was last
// polled, and the "later" matters: the task was encoded at the end of the
// previous poll, from that poll's pre-apply status, so its bound can be one
// poll stale, and a stale bound that simply overrode the live one would page
// further back on every single poll forever.
//
// An RSS feed is ordered newest-first, so a page whose rows predate the bound
// holds nothing the previous poll did not already see. It is a paging bound
// only -- page 0 is always read in full -- so a feed that is not strictly
// ordered loses nothing.
func pollSince(idx *indexv1alpha1.Indexer, task schema.RssTask) *time.Time {
	var out *time.Time
	if task.Since != nil && !task.Since.IsZero() {
		out = task.Since
	}
	if at := idx.Status.LastRssAt; at != nil && !at.Time.IsZero() {
		if out == nil || at.After(*out) {
			out = new(at.Time)
		}
	}
	return out
}

// reachedSince reports whether batch has run back past since.
func reachedSince(batch []torznab.Release, since *time.Time) bool {
	if since == nil {
		return false
	}
	for _, r := range batch {
		if !r.PubDate.IsZero() && !r.PubDate.After(*since) {
			return true
		}
	}
	return false
}
