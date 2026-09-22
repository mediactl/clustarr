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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexer"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
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
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// heartbeatInterval is how often the poll extends its ack deadline.
	// ConsumerIndexRSS's AckWait is 60s, pinned to the pod's
	// terminationGracePeriodSeconds; the topology's own rule is that work
	// which can outlast the grace period heartbeats rather than raising
	// AckWait past it. 20s leaves two missed beats of headroom.
	//
	// This is NOT Subscription.Heartbeat, which is the broker's idle
	// heartbeat for connection liveness and extends nothing.
	heartbeatInterval = 20 * time.Second

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

	// defaultRssInterval mirrors spec.rssInterval's
	// +kubebuilder:default="15m". It is restated here, rather than relied on
	// from the apiserver, because that default reaches far fewer objects than
	// it looks like it does -- see rssInterval.
	// TestRssIntervalDefaultMatchesTheGeneratedCRD reads the generated schema
	// and fails if the two drift, so this is a mirror and not a second source
	// of truth.
	defaultRssInterval = 15 * time.Minute
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

	// Bus carries the release firehose and the next scheduled poll.
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
	if !indexer.Healthy(idx.Status, now) {
		at := idx.Status.DisabledUntil.Time
		log.Debug("rss: indexer is backing off; not querying", "until", at)
		if err := ScheduleNext(ctx, w.Deps.Bus, &idx, at); err != nil {
			return events.Retry(statusRetry, err)
		}
		return nil
	}

	fetched, pollErr := w.pollOnce(ctx, m, &idx, task, now)

	if pollErr != nil && (errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded)) {
		// The pod is going away mid-poll. This is our shutdown, not the
		// indexer's fault: recording a failure here would escalate every
		// indexer in the namespace on every rollout, and the ladder would
		// then disable them for minutes. Nak and let the next pod take it;
		// nothing has been indexed or published, so redelivery is a clean
		// retry.
		return events.Retry(retryAfterShutdown, pollErr)
	}

	var (
		mutate func(*indexac.IndexerStatusApplyConfiguration)
		esc    indexer.Escalation
	)
	if pollErr != nil {
		esc = indexer.RecordFailure(idx.Status, now, pollErr.Error())
		mutate = func(ac *indexac.IndexerStatusApplyConfiguration) {
			applyEscalation(ac, esc, idx.Status)
		}
	} else {
		inserted, published, err := w.indexAndPublish(ctx, &idx, fetched, now)
		if err != nil {
			return events.Retry(statusRetry, err)
		}
		log.Info("rss: poll complete", "fetched", len(fetched), "inserted", inserted, "published", published)

		esc = indexer.RecordSuccess(idx.Status, now)
		mutate = func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithLastRssAt(metav1.NewTime(now)).
				WithLastRssNewCount(int32(inserted)).
				// A read-modify-write, deliberately without a CAS loop.
				// relindex.Store is fixed at four methods and Stats has no
				// per-indexer breakdown, so a running total is the only
				// source. MaxAckPending is 4, so two concurrent polls of the
				// SAME indexer need a duplicate schedule to fire; the lost
				// update is then at most one poll's inserted count and it
				// self-corrects at the next poll.
				WithIndexedReleases(idx.Status.IndexedReleases + int64(inserted))
			applyEscalation(ac, esc, idx.Status)
		}
	}

	// ONE apply, whatever happened, and it goes through indexarr/status so
	// the indexarr-worker owned set is declared in exactly one place. Never
	// an early return with a partial status: the early return is usually the
	// transient case, which is exactly when a healthy object would be gutted
	// by a blip.
	if err := idxstatus.Patch(ctx, w.Deps.Client, k8s.ManagerIndexarrWorker, &idx, mutate); err != nil {
		return events.Retry(statusRetry, err)
	}

	// Reschedule BEFORE returning the poll error, so a failing indexer keeps
	// its cadence and recovers on its own rather than waiting for the next
	// reconcile.
	if err := ScheduleNext(ctx, w.Deps.Bus, &idx, now.Add(rssInterval(&idx))); err != nil {
		return events.Retry(statusRetry, err)
	}
	if pollErr != nil {
		return events.Retry(retryAfterFailure(esc, now), pollErr)
	}
	return nil
}

// applyEscalation declares the five escalation fields on ac.
//
// The pointer fields are ASSIGNED rather than set through the generated
// With* helpers, because a With* helper cannot express "clear this": the
// apply configuration comes pre-seeded from the live status by
// indexarr/status.WorkerFields, so a recovered indexer whose disabledUntil is
// now nil must have that seed removed, not carried forward.
//
// InitialFailureAt comes from Escalation.InitialFailure, which defines the
// 0 -> 1 transition beside the ladder that defines every other transition.
func applyEscalation(
	ac *indexac.IndexerStatusApplyConfiguration,
	esc indexer.Escalation,
	cur indexv1alpha1.IndexerStatus,
) {
	ac.WithEscalationLevel(esc.FailureLevel).WithLastFailure(esc.LastFailureMsg)
	ac.DisabledUntil = esc.DisabledUntil
	ac.LastFailureAt = esc.LastFailureAt
	ac.InitialFailureAt = esc.InitialFailure(cur)
}

// retryAfterFailure derives the redelivery delay from the escalation the
// failure produced, so the redelivery lands when the ladder next permits a
// query rather than on a schedule that knows nothing about it.
func retryAfterFailure(esc indexer.Escalation, now time.Time) time.Duration {
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
func (w *Worker) pollOnce(
	ctx context.Context,
	m events.Message,
	idx *indexv1alpha1.Indexer,
	task schema.RssTask,
	now time.Time,
) ([]torznab.Release, error) {
	if w.Deps.SearcherFor == nil {
		return nil, errors.New("rss: no SearcherFor was wired")
	}
	s, err := w.Deps.SearcherFor(ctx, idx)
	if err != nil {
		return nil, fmt.Errorf("rss: build search client: %w", err)
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
			return all, ctx.Err()
		}
		if beat := w.now(); last.IsZero() || beat.Sub(last) >= heartbeatInterval {
			last = beat
			if err := m.InProgress(ctx); err != nil {
				return all, fmt.Errorf("rss: heartbeat: %w", err)
			}
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
			return all, err
		}
		// One page is one query, which is the unit this histogram documents.
		metrics.IndexerReleasesReturned.WithLabelValues(idx.Name).Observe(float64(len(batch)))

		all = append(all, batch...)
		if len(batch) < pageSize || reachedSince(batch, since) {
			// RssTask.Since exists so the worker can stop paging early.
			break
		}
	}
	return all, nil
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
) (inserted, published int, err error) {
	protocol := idx.Status.Protocol
	if protocol == "" && idx.Spec.Generic != nil {
		// A generic upstream declares its protocol in the spec, so a poll
		// that beats the first reconcile still ships a usable release rather
		// than one the consumer cannot route.
		protocol = idx.Spec.Generic.Protocol
	}

	rows := make([]relindex.Release, 0, len(fetched))
	projected := make([]schema.Release, 0, len(fetched))
	for _, r := range fetched {
		rel := ProjectRelease(r, idx.Name, string(protocol))
		rel.FetchedAt = now
		projected = append(projected, rel)

		row, rowErr := indexRow(rel, idx.Name, now)
		if rowErr != nil {
			return 0, 0, rowErr
		}
		rows = append(rows, row)
	}

	// Upsert FIRST, and take lastRssNewCount from its truthful `inserted`.
	// Every fetched row is offered; the store decides what is new. Counting
	// len(fetched) instead would report 100 new releases every 15 minutes on
	// a feed that has not moved.
	if w.Deps.Index != nil && len(rows) > 0 {
		inserted, err = w.Deps.Index.Upsert(ctx, rows)
		if err != nil {
			return 0, 0, fmt.Errorf("rss: index %d releases: %w", len(rows), err)
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
		published, err = PublishReleases(ctx, w.Deps.Bus, idx.Namespace, idx.Name, projected)
		if err != nil {
			return inserted, published, err
		}
	}
	return inserted, published, nil
}

// indexRow renders one projected release as an index row. InfoJSON is the
// whole schema.Release, so a replay needs no re-query.
func indexRow(rel schema.Release, indexerName string, now time.Time) (relindex.Release, error) {
	raw, err := json.Marshal(rel)
	if err != nil {
		return relindex.Release{}, fmt.Errorf("rss: encode index row for %s/%s: %w",
			indexerName, rel.Info.GUID, err)
	}
	// TitleNorm is release.CleanTitle, NOT release.Normalize. relindex
	// stores what it is given and escapes Query.Text without normalising it,
	// so the indexed column and the query must go through ONE function or
	// the index answers nothing -- and pkg/relindex cannot catch a mismatch
	// from the inside. Task D1-2's contract names CleanTitle, which is also
	// the only candidate in the tree that is documented for equality
	// comparison: release.Normalize's own doc says it preserves case and is
	// "for display / re-embedding ... not for equality comparison".
	row := relindex.Release{
		Indexer:    indexerName,
		GUID:       rel.Info.GUID,
		Title:      rel.Info.Title,
		TitleNorm:  release.CleanTitle(rel.Info.Title),
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
		row.PublishedAt = ptr.To(p.Time)
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
			out = ptr.To(at.Time)
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

// rssInterval is how long until the next poll of idx, floored at the CRD's
// own default.
//
// The floor is load-bearing, and the obvious reading of why it is not needed
// is wrong. An apiserver default fills a field that is ABSENT FROM THE
// SUBMITTED JSON. metav1.Duration is a struct and `omitempty` does nothing to
// a struct field, so a typed Go client ALWAYS marshals it: an Indexer created
// through client-go sends `"rssInterval":"0s"` explicitly and is never
// defaulted. Only YAML and unstructured creates -- kubectl apply, the chart
// -- omit the key and get 15m.
//
// Unfloored, that zero schedules the next poll at `now`, which is
// immediately redeliverable: one indexer would be polled as fast as the
// broker could hand the task back.
//
// This is the opposite call from spec.requestDelay, which the Indexer
// reconciler deliberately does NOT floor, because ratelimit.Config documents
// RPS <= 0 as unlimited and an explicit `requestDelay: 0s` is therefore a
// supported "do not pace me". A zero poll interval has no such reading --
// nobody is asking to poll an indexer infinitely often -- so the two must not
// be generalised into one rule.
func rssInterval(idx *indexv1alpha1.Indexer) time.Duration {
	if d := idx.Spec.RssInterval.Duration; d > 0 {
		return d
	}
	return defaultRssInterval
}

// TaskMsgID is the deduplication id for one scheduled poll slot.
//
// It carries the slot, not just the object, on purpose: CLUSTARR_WORK_INDEXARR
// dedups for 1h, so a constant per-object id would make every schedule after
// the first within that hour a no-op and the indexer would silently stop
// polling. Two schedulers converging on the same slot -- the reconciler
// seeding one while a poll schedules the next -- collapse to one delivery,
// which is exactly what we want.
func TaskMsgID(uid string, generation int64, slot time.Time) string {
	return events.MsgIDForObject(uid, generation,
		"rss:"+slot.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// ScheduleNext publishes the next RssTask for idx, held on the schedule
// subject until at.
//
// The reconciler seeds the first one with this same function so the two
// cannot build the subject or the msg-id two ways. The worker schedules the
// next one at the end of EVERY poll, success or failure: if only the
// reconciler scheduled, polling would stop until the next reconcile.
func ScheduleNext(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, at time.Time) error {
	if idx.UID == "" {
		// The work subject is keyed by uid. An empty token would publish to
		// a subject no stream claims, which the bus reports only as a
		// publish error at the far end of a poll that already succeeded.
		return fmt.Errorf("rss: schedule %s/%s: the Indexer has no UID", idx.Namespace, idx.Name)
	}
	name, data, err := schema.Encode(schema.RssTask{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
		Categories: idx.Spec.Categories,
		Since:      newestSeen(idx),
	})
	if err != nil {
		return fmt.Errorf("rss: encode schedule for %s/%s: %w", idx.Namespace, idx.Name, err)
	}
	id := TaskMsgID(string(idx.UID), idx.Generation, at)
	env := &events.Envelope{
		ID:     id,
		Type:   "index.RssTask",
		Schema: name,
		Source: "indexarr@" + version.String(),
		// The work-task envelope key is the same <namespace>/<name> shape as
		// the firehose's, for the same reason: whoever handles it cuts it to
		// recover the namespace.
		Key:  idx.Namespace + "/" + idx.Name,
		Time: at,
		Data: data,
	}
	tracing.Inject(ctx, env)

	// Publish to the TARGET subject; the bus rewrites it onto the schedule
	// holding subject and asks the broker to republish it to the target when
	// the schedule fires. Publishing to the holding subject directly would
	// make the message re-trigger itself.
	_, err = bus.Publish(ctx, events.WorkRSSSubject(string(idx.UID)), env,
		events.WithMsgID(id), events.WithScheduleAt(at))
	if err != nil {
		return fmt.Errorf("rss: schedule %s/%s at %s: %w", idx.Namespace, idx.Name, at, err)
	}
	return nil
}

// newestSeen is the publish time the next poll may stop paging at. Only
// status.lastRssAt records how far this indexer has been read; there is no
// field carrying the newest PUBLISH time, and inventing one would be a CRD
// change no Phase D1 task owns.
func newestSeen(idx *indexv1alpha1.Indexer) *time.Time {
	if at := idx.Status.LastRssAt; at != nil && !at.Time.IsZero() {
		return ptr.To(at.Time)
	}
	return nil
}
