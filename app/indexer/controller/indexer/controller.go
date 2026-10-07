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

package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/app/indexer/rssschedule"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

const (
	// reprobeInterval is the steady-state tick. It is short because the
	// Healthy condition is derived from worker-owned status fields that
	// k8s.GenerationChanged() deliberately filters out of the watch, so this
	// tick is what refreshes it. The window counts behind RateLimited are
	// read off the clustarr-indexer-limits rings, whose every change also
	// enqueues the Indexer (limitsSource).
	reprobeInterval = 15 * time.Minute

	// capsTTL is how stale status.caps may get before the tick spends an
	// actual HTTP request on t=caps. An indexer's capability set changes
	// roughly never; its rate limit is a real budget.
	capsTTL = 12 * time.Hour
)

// Reconciler reconciles an Indexer: it validates the spec, probes caps and
// derives the conditions. It is the ONLY writer of status.conditions,
// .protocol, .privacy, .caps, .observedGeneration and .sessionSecretRef, and
// it writes NONE of the escalation or counter fields -- see this package's
// doc comment.
type Reconciler struct {
	Client client.Client

	// Recorder is optional; a nil Recorder disables events rather than
	// panicking, which is what lets a unit test construct a Reconciler with
	// nothing but a client. It is an events.k8s.io/v1 recorder, from
	// mgr.GetEventRecorder, matching indexarr's other two controllers.
	Recorder k8sevents.EventRecorder

	// Limiters paces this process's own requests to each indexer host: the
	// caps probe and the Cardigann login. Since the manager/agent split it is
	// the manager's own instance (app/indexer/manager). The index agent's
	// ClientCache paces searches, RSS polls and grabs on another (§5.12), and
	// in the worst case this adds one request per host per capsTTL inside the
	// agent's gap, which R8 accepts.
	//
	// Like Recorder, it is optional: a nil Limiters disables pacing rather
	// than panicking, which is what lets a unit test construct a
	// Reconciler with nothing but a client.
	//
	// Remove() is deliberately never called, not even when an Indexer is
	// deleted. The key is a HOST, not an object, and several Indexers
	// pointing at one host is the expected topology rather than the
	// exception -- a Prowlarr or Jackett instance in front of many
	// trackers, or a torrent and a usenet Indexer on one server. Dropping
	// the bucket when any one of them goes away would leave the survivors
	// drawing on the Limiter's *default* config, which is unlimited,
	// until each happened to reconcile again: up to a full reprobeInterval
	// of hammering a host that asked for one request every two seconds.
	// The cost of not pruning is one map entry per distinct host, which is
	// a handful of bytes bounded by the size of the cluster's indexer set.
	Limiters *ratelimit.Limiter

	// Bus seeds the RSS poll chain (ruling R36). Nothing else in this
	// reconciler publishes.
	//
	// Like Recorder and Limiters it is optional, so a unit test can build a
	// Reconciler with nothing but a client -- but unlike them, a nil Bus
	// disables something an operator would notice: the indexer would never
	// be polled at all. It therefore logs a warning every time it would have
	// seeded, rather than skipping in silence.
	Bus events.Bus

	// Sessions persists Cardigann login sessions (clustarr-indexer-sessions
	// KV plus the owned Secret). NewReconciler builds it from the bus; nil
	// means the Secret alone.
	Sessions *idxclients.SessionStore

	// Now is the clock; nil means time.Now. The window projection reads the
	// rings at this instant, so a test can roll a full window past without
	// waiting a day.
	Now func() time.Time

	mu       sync.Mutex
	capsSeen map[types.UID]capsMemo
}

// capsMemo records when this process last probed an Indexer's caps, and at
// which generation. It lives in memory because IndexerStatus has no
// capsFetchedAt field and inventing one is an API change with one consumer --
// a process restart simply re-probes once, which is harmless and arguably
// desirable.
type capsMemo struct {
	at         time.Time
	generation int64
}

// NewReconciler builds a Reconciler. recorder comes from
// mgr.GetEventRecorder and writes events.k8s.io/v1 Events; limiters is the
// manager's own *ratelimit.Limiter, pacing the caps probe and the logins (the
// index agent paces its searches, polls and grabs on another, §5.12); bus is
// the one the RSS worker consumes from, and is what lets this reconciler seed
// the first poll and watch the sessions the index agent drops. Its session
// store writes as indexarr.
func NewReconciler(
	c client.Client,
	recorder k8sevents.EventRecorder,
	limiters *ratelimit.Limiter,
	bus events.Bus,
) *Reconciler {
	return &Reconciler{
		Client:   c,
		Recorder: recorder,
		Limiters: limiters,
		Bus:      bus,
		Sessions: idxclients.NewSessionStore(c, bus, k8s.ManagerIndexarr),
		capsSeen: map[types.UID]capsMemo{},
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) shouldProbe(uid types.UID, generation int64, hasCaps bool, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.capsSeen[uid]
	return !ok || !hasCaps || m.generation != generation || now.Sub(m.at) >= capsTTL
}

func (r *Reconciler) markProbed(uid types.UID, generation int64, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capsSeen[uid] = capsMemo{at: now, generation: generation}
}

func (r *Reconciler) forget(uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.capsSeen, uid)
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "indexer.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("indexer", req.NamespacedName)
	now := r.now()

	var idx indexv1alpha1.Indexer
	if err := r.Client.Get(ctx, req.NamespacedName, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			// The object is already gone, so its UID -- the caps memo's
			// key -- is not recoverable and nothing can be pruned here.
			// Indexer carries no finalizer, so this is in fact the usual
			// delete path and the DeletionTimestamp branch below only
			// fires while something else holds the object open. The memo
			// therefore leaks one 24-byte entry per Indexer this process
			// ever saw deleted, which is operator-driven and small. Keying
			// it by name instead would prune here and would be worse: an
			// Indexer deleted and recreated under the same name is a
			// different object, and it would inherit a warm memo and skip
			// the caps probe it needs.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !idx.DeletionTimestamp.IsZero() {
		// The caps memo is keyed by UID and is safe to prune. The limiter
		// bucket is keyed by HOST and is not -- see the Limiters field's
		// comment. The index agent's client cache is in another process and
		// prunes a deleted Indexer itself (clientcache.ClientCache.Prune).
		r.forget(idx.UID)
		return ctrl.Result{}, nil
	}

	conditions := append([]metav1.Condition(nil), idx.Status.Conditions...)

	// What this pass resolves is written onto the LOCAL copy of the status
	// first, and app/indexer/status.ControllerFields then seeds the apply from
	// that copy. The apply is therefore a complete declaration of the
	// indexarr-owned set by construction, on every return path, rather than
	// by a discipline remembered at seven call sites -- so an early return
	// on a transient failure re-sends the caps, protocol and privacy that
	// are already there instead of releasing them.
	idx.Status.ObservedGeneration = idx.Generation
	idx.Status.SessionSecretRef = idxclients.SessionSecretName(idx.Name)
	// queriesInWindow and grabsInWindow too, on every path: they are this
	// manager's, projected from the rings at this instant, early returns
	// included.
	windows := r.projectWindows(ctx, &idx, now)

	kind, err := idxclients.ResolveSource(idx.Spec)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		if _, perr := r.patch(ctx, &idx, conditions, ctrl.Result{}); perr != nil {
			return ctrl.Result{}, perr
		}
		// Terminal: no amount of retrying fixes a spec with the wrong
		// number of sources, and a hot loop on a typo burns an apiserver.
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	if idx.Spec.Enabled != nil && !*idx.Spec.Enabled {
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
		// No requeue: only a spec edit can re-enable it, and
		// k8s.GenerationChanged() already delivers that.
		return r.patch(ctx, &idx, conditions, ctrl.Result{})
	}

	if kind != idxclients.SourceGeneric {
		return r.reconcileDefinition(ctx, &idx, conditions, windows, now)
	}

	secret, err := idxclients.ReadSecret(ctx, r.Client, idx.Namespace, idx.Spec.SecretRef)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkUnknown(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonDependencyNotReady, "%s", err.Error())
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
		return r.patch(ctx, &idx, conditions, ctrl.Result{RequeueAfter: reprobeInterval})
	}
	idx.Status.Protocol = idxclients.ProtocolFor(kind, idx.Spec)
	idx.Status.Privacy = idxclients.PrivacyFor(kind, secret)

	// The bucket config for this host, written here and nowhere else: this
	// reconciler is the only reader of spec.requestDelay, and the search
	// fan-out, the RSS poll and the download verb only Wait on the same
	// *ratelimit.Limiter. It used to live inside BuildClient, which
	// ClientCache now shares -- see ApplyRateLimit.
	idxclients.ApplyRateLimit(idx.Spec, nil, r.Limiters, 0)

	transport, err := idxclients.ResolveProxy(ctx, r.Client, &idx)
	if err != nil {
		return r.proxyUnavailable(ctx, &idx, conditions, err)
	}

	tc, endpoint, err := idxclients.BuildClient(idx.Spec, secret, r.Limiters, transport)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		if _, perr := r.patch(ctx, &idx, conditions, ctrl.Result{}); perr != nil {
			return ctrl.Result{}, perr
		}
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	outcome := idxclients.ProbeOutcome{}
	probed := false
	if r.shouldProbe(idx.UID, idx.Generation, idx.Status.Caps != nil, now) {
		probed = true
		start := time.Now()
		wire, perr := tc.Caps(ctx)
		// The `function` label is the Torznab t= value, and t=caps is not
		// a SearchMode -- hence the literal. `indexer` is the OBJECT NAME:
		// never a title, a host or an indexer-supplied string.
		metrics.IndexerQueryDuration.WithLabelValues(idx.Name, "caps").Observe(time.Since(start).Seconds())
		outcome = idxclients.Classify(perr)
		metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, idxclients.OutcomeLabel(outcome)).Inc()
		if perr == nil {
			// Folded in on success ONLY. A failed probe leaves the
			// previously probed caps exactly where they are; this is the
			// difference between "the indexer is briefly unreachable" and
			// "the indexer's capabilities were reset to nothing".
			projected := projectCaps(wire)
			idx.Status.Caps = &projected
			r.markProbed(idx.UID, idx.Generation, now)
		} else {
			tracing.RecordError(span, perr)
			log.Warn("caps probe failed", "host", endpoint.Host, "reason", outcome.Reason, "error", perr)
		}
		// The probe was a network round trip. Re-read before deriving the
		// conditions from the worker's fields and applying.
		if gone, err := r.refreshWorkerFields(ctx, &idx); err != nil || gone {
			return ctrl.Result{}, err
		}
	}

	return r.finish(ctx, &idx, conditions, windows, probed, outcome, r.now())
}

// finish derives Authenticated, RateLimited, Healthy and Ready from what this
// pass learned, seeds the RSS chain and applies. It is the shared tail of the
// generic and the definition-backed paths, so the two cannot disagree about
// what a failed probe or a failed login means for Ready.
func (r *Reconciler) finish(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	conditions []metav1.Condition,
	windows windowRetry,
	probed bool,
	outcome idxclients.ProbeOutcome,
	now time.Time,
) (ctrl.Result, error) {
	switch {
	case !probed || outcome.Reason == "":
		k8s.MarkTrue(idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonReconciled, "credentials accepted")
	case outcome.AuthFailed:
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, outcome.Reason, "%s", outcome.Message)
		if r.Recorder != nil {
			r.Recorder.Eventf(idx, nil, corev1.EventTypeWarning, outcome.Reason,
				"Reconcile", "the indexer rejected the configured credentials: %s", outcome.Message)
		}
	default:
		// Always set, never left absent: a condition this manager
		// sometimes sends and sometimes omits is released on the apply
		// that omits it.
		k8s.MarkUnknown(idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, outcome.Reason, "%s", outcome.Message)
	}

	limited, limitMsg := rateLimited(idx.Spec, idx.Status)
	if outcome.Limited {
		limited, limitMsg = true, outcome.Message
	}
	if limited {
		k8s.MarkTrue(idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, idxclients.ReasonLimitReached, "%s", limitMsg)
	} else {
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, k8s.ReasonReconciled, "under the configured limits")
	}

	healthy := idxstatus.Healthy(idx.Status, now) && outcome.Reason == ""
	switch {
	case !idxstatus.Healthy(idx.Status, now):
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionHealthy, idxclients.ReasonBackingOff,
			"backing off until %s (escalation level %d)",
			idx.Status.DisabledUntil.Time.UTC().Format(time.RFC3339), idx.Status.EscalationLevel)
	case outcome.Reason != "":
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionHealthy, outcome.Reason, "%s", outcome.Message)
	default:
		k8s.MarkTrue(idx, &conditions, indexv1alpha1.IndexerConditionHealthy, k8s.ReasonReconciled, "last request succeeded")
	}

	// RateLimited deliberately does NOT clear Ready. A daily query budget
	// is an Indexer's expected steady state, and flapping Ready once a day
	// is noise an operator learns to ignore. (This differs from
	// MetadataProvider, where Throttled does clear Ready; that provider's
	// throttle is an exception, not a budget.)
	switch {
	case healthy && idx.Status.Caps != nil:
		k8s.MarkReady(idx, &conditions, true, k8s.ReasonReconciled, "reachable, caps probed")
	case idx.Status.Caps == nil:
		k8s.MarkReady(idx, &conditions, false, idxclients.ReasonProbeFailed, "caps have never been probed successfully")
	default:
		k8s.MarkReady(idx, &conditions, false,
			firstNonEmpty(outcome.Reason, idxclients.ReasonBackingOff),
			"%s", firstNonEmpty(outcome.Message, "backing off"))
	}

	// Seed BEFORE the patch, but do not return on its error: the status
	// write is unconditional on every path through Reconcile, and an early
	// return here would be exactly the partial-status bug this package is
	// built to avoid. The bus failure is surfaced after the write instead.
	seedErr := r.seedRSSSchedule(ctx, idx, healthy, windows.query, now)

	res, err := r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: r.requeueAfter(idx.Status, outcome, windows, now)})
	if err != nil {
		return res, err
	}
	if seedErr != nil {
		// Returned as an error rather than folded into RequeueAfter: a bus
		// that refused the publish should be retried with the controller's
		// backoff, not in fifteen minutes, because until it succeeds this
		// indexer is not being polled at all.
		return ctrl.Result{}, seedErr
	}
	return res, nil
}

// seedRSSSchedule starts this Indexer's RSS poll chain.
//
// Nothing else does. The worker schedules the NEXT poll at the end of every
// poll it performs, which keeps an already-running chain alive but cannot
// begin one -- so before ruling R36 the release firehose never started in a
// real cluster, and the worker's disabled path, which acknowledges without
// rescheduling precisely because "re-enabling bumps the generation and the
// reconciler seeds a fresh schedule", silenced an indexer permanently the
// first time an operator toggled spec.enabled.
//
// The gate is the worker's own gate, spelled the same way, so the seed and
// the poll agree on what "this indexer polls" means. The spec.enabled half is
// also checked by Reconcile, which returns before reaching here; it is
// repeated rather than assumed because the two conditions are one rule.
//
// Publishing on EVERY reconcile is safe, and deliberately so rather than
// guarded by a "have I seeded this one?" memo, which would be a second
// in-memory truth about a cluster-wide fact and would be wrong after every
// restart. rssschedule.NextPollAt returns the slot the worker's own reschedule
// already chose, rssschedule.TaskMsgID quantises it to the second, and
// CLUSTARR_WORK_INDEXARR deduplicates for an hour (pkg/events/topology.go's
// work() helper, Duplicates: time.Hour), so the duplicate seed stores
// nothing. Where it does store something -- a slot past the dedup window, or
// a genuinely new slot -- the scheduled publish carries Nats-Rollup: sub, so
// it REPLACES the pending poll rather than adding a second one.
//
// queryRetry is when a full query window next has room, zero when it has
// room now. A poll seeded before then would only be refused by its own
// reservation, so the seed waits for it.
func (r *Reconciler) seedRSSSchedule(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	healthy bool,
	queryRetry time.Time,
	now time.Time,
) error {
	if !ptr.Deref(idx.Spec.Enabled, true) || !ptr.Deref(idx.Spec.EnableRss, true) {
		return nil
	}
	if !healthy {
		// Backing off, or the caps probe just failed. The ladder's window
		// exists to stop queries, and the worker's own backoff path already
		// holds the chain open by scheduling the poll that lands when the
		// window expires. requeueAfter brings this reconciler back a second
		// after disabledUntil, which is when seeding becomes useful again.
		return nil
	}
	if r.Bus == nil {
		logging.FromContext(ctx).Warn(
			"no bus is wired; this Indexer's RSS poll chain cannot be seeded and it will never be polled",
			"indexer", client.ObjectKeyFromObject(idx))
		return nil
	}
	at := rssschedule.NextPollAt(idx, now)
	if queryRetry.After(at) {
		at = queryRetry
	}
	return rssschedule.ScheduleNext(ctx, r.Bus, idx, at)
}

// rateLimited derives the RateLimited condition from spec.limits and
// status.queriesInWindow / status.grabsInWindow, which projectWindows has
// just read off the rings. Prowlarr counts IndexerQuery + IndexerRss
// together against QueryLimit; both reserve on the query ring, one entry per
// request. A limit of 0 or less is none, as Prowlarr reads it.
func rateLimited(spec indexv1alpha1.IndexerSpec, st indexv1alpha1.IndexerStatus) (bool, string) {
	if spec.Limits == nil {
		return false, ""
	}
	unit := spec.Limits.Unit
	if unit == "" {
		unit = indexv1alpha1.LimitUnitDay
	}
	idx := &indexv1alpha1.Indexer{Spec: spec}
	if l := limits.QueryLimit(idx); l != nil && st.QueriesInWindow >= *l {
		return true, fmt.Sprintf("queries %d/%d per %s", st.QueriesInWindow, *l, unit)
	}
	if l := limits.GrabLimit(idx); l != nil && st.GrabsInWindow >= *l {
		return true, fmt.Sprintf("grabs %d/%d per %s", st.GrabsInWindow, *l, unit)
	}
	return false, ""
}

// windowRetry is when each full window next has room, zero for a window
// with room now, no limit, or a ring that could not be read.
type windowRetry struct {
	query, grab time.Time
}

// earliest is the sooner of the two non-zero instants, zero when neither is
// set.
func (w windowRetry) earliest() time.Time {
	switch {
	case w.query.IsZero():
		return w.grab
	case w.grab.IsZero() || w.query.Before(w.grab):
		return w.query
	default:
		return w.grab
	}
}

// projectWindows sets status.queriesInWindow and status.grabsInWindow from
// idx's clustarr-indexer-limits rings at now, and returns when each full
// window next has room.
//
// The rings are the truth and these two fields their projection; this
// reconciler owns them (app/indexer/status's package doc), and reading the
// ring on every pass is what brings a count down as its window rolls on
// even when nothing counts -- which is the case that matters, an indexer
// that hit its limit and went quiet. A ring that cannot be read leaves its
// field as it stands rather than releasing or zeroing it: an outage is not
// an empty window.
func (r *Reconciler) projectWindows(ctx context.Context, idx *indexv1alpha1.Indexer, now time.Time) windowRetry {
	var w windowRetry
	if r.Bus == nil || idx.UID == "" {
		return w
	}
	kv := r.Bus.KV(events.BucketIndexerLimits)
	log := logging.FromContext(ctx)
	if q, err := limits.Queries(ctx, kv, idx, now); err != nil {
		log.Warn("indexer: reading the query ring failed; keeping queriesInWindow", "error", err)
	} else {
		idx.Status.QueriesInWindow, w.query = q.Count, q.RetryAt
	}
	if g, err := limits.Grabs(ctx, kv, idx, now); err != nil {
		log.Warn("indexer: reading the grab ring failed; keeping grabsInWindow", "error", err)
	} else {
		idx.Status.GrabsInWindow, w.grab = g.Count, g.RetryAt
	}
	return w
}

// requeueAfter picks the next tick. It never returns 0, because a zero
// RequeueAfter means "do not requeue" and this reconciler's Healthy condition
// is derived from worker-owned fields the watch predicate filters out -- only
// the tick refreshes it. It is always RequeueAfter, never a bare Requeue and
// never an error returned purely to force a retry.
//
// A full limit window brings it back the moment the window next has room,
// when that is sooner: that pass reads the lower count and clears
// RateLimited with no traffic at all, which an indexer at its limit by
// definition has none of.
func (r *Reconciler) requeueAfter(st indexv1alpha1.IndexerStatus, outcome idxclients.ProbeOutcome, windows windowRetry, now time.Time) time.Duration {
	// A server's Retry-After is authoritative; pkg/ratelimit.Backoff
	// documents the same rule for the same reason.
	if outcome.RetryAfter > 0 {
		return outcome.RetryAfter
	}
	d := reprobeInterval
	if st.DisabledUntil != nil && now.Before(st.DisabledUntil.Time) {
		d = st.DisabledUntil.Sub(now) + time.Second
	}
	if at := windows.earliest(); at.After(now) && at.Sub(now) < d {
		d = at.Sub(now)
	}
	return d
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// patch is the ONLY status write in this package. Every return path in
// Reconcile goes through it; do not add a second one.
//
// It routes through app/indexer/status.Patch (ruling R31), which holds the one
// declaration of what k8s.ManagerIndexarr owns on Indexer.status and seeds
// the apply configuration from idx.Status. Conditions are supplied here and
// ONLY here: ControllerFields deliberately does not seed them, and the
// generated WithConditions appends rather than replaces, so setting them in
// both places is rejected with `duplicate entries for key [type="Ready"]`.
func (r *Reconciler) patch(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	conditions []metav1.Condition,
	result ctrl.Result,
) (ctrl.Result, error) {
	// The DLQ projector's annotation becomes the DeadLettered condition here,
	// the one place every apply's conditions are declared, so it is part of
	// this manager's complete set on every path -- early returns included --
	// and is removed once an operator deletes the annotation.
	k8s.MarkDeadLettered(idx, &conditions)
	err := idxstatus.Patch(ctx, r.Client, k8s.ManagerIndexarr, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithConditions(k8s.ConditionACs(conditions)...)
		})
	if err != nil {
		logging.FromContext(ctx).Error("patch status", "error", err)
		return ctrl.Result{}, err
	}
	return result, nil
}

// The +kubebuilder:rbac markers for this package live in doc.go, at package
// level. controller-gen collects them only from a comment group that is not
// attached to a declaration, and a block placed here, above
// SetupWithManager, becomes that function's doc comment and is silently
// discarded -- which is exactly how four Phase C controllers shipped with no
// permissions at all.

// SetupWithManager registers the Indexer controller. Task D1-8 calls
// NewReconciler(...).SetupWithManager(mgr); see doc.go.
//
// The For() predicate is generation-filtered -- worker-owned status churn
// must not re-run a caps probe -- with the DLQ projector's annotation OR-ed
// in, or an annotation-only change would never reach the DeadLettered fold.
//
// IndexerDefinition is watched so a definition edit reaches the Indexers that
// use it at once rather than at their next reprobe tick (up to 15 minutes),
// and so an Indexer created before its definition resolves the moment the
// definition appears. The watch passes a definition's creation, deletion and
// spec edits, and a change of the status.id or status.replaces that
// spec.definition resolves by; [indexersForDefinition] maps each onto the Indexers that name it.
//
// IndexerProxy is watched (spec edits only -- its own controller's status
// writes are not a routing change) for the same reason, mapped by
// [indexersForProxy] onto the Indexers it applies to.
//
// With a bus, every change to an Indexer's clustarr-indexer-limits rings
// enqueues it too ([Reconciler.limitsSource]), so the window counts on status
// follow the traffic rather than the 15-minute tick. With a bus it also
// watches clustarr-indexer-sessions ([Reconciler.sessionsSource]), so a
// session the index agent dropped is logged in again at once.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("indexer").
		For(&indexv1alpha1.Indexer{}, builder.WithPredicates(
			k8s.Or(k8s.GenerationChanged(), k8s.DeadLetteredAnnotationChanged()))).
		Watches(&indexv1alpha1.IndexerDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.indexersForDefinition),
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(),
				k8s.StatusFieldChanged(definitionIDs)))).
		Watches(&indexv1alpha1.IndexerProxy{},
			handler.EnqueueRequestsFromMapFunc(r.indexersForProxy),
			builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{
			ReconciliationTimeout: 5 * time.Minute,
			RecoverPanic:          ptr.To(true),
		})
	if r.Bus != nil {
		b = b.WatchesRawSource(r.limitsSource()).WatchesRawSource(r.sessionsSource())
	}
	return b.Complete(r)
}

// limitsSource enqueues the Indexer whose query or grab ring just changed in
// clustarr-indexer-limits.
//
// The rings change on every reservation, from any replica's search fan-out,
// RSS poll or download verb, or catalogarr's grab path; none of them write
// the counts on status any more, so without this the projection would wait
// for the 15-minute tick -- and RateLimited with it, which a full window is
// exactly when an operator looks. A refused reservation writes nothing and
// so wakes nothing; the requeue at the window's retry time covers the other
// direction. The watch replays every ring at start, which reconciles each
// Indexer that has one once, as the informer's initial list already does.
func (r *Reconciler) limitsSource() source.Source {
	return source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		ch, err := r.Bus.KV(events.BucketIndexerLimits).Watch(ctx, ">")
		if err != nil {
			return fmt.Errorf("indexer: watch %s: %w", events.BucketIndexerLimits, err)
		}
		go func() {
			for e := range ch {
				for _, req := range r.indexersForRing(ctx, e.Key) {
					q.Add(req)
				}
			}
			if ctx.Err() == nil {
				logging.FromContext(ctx).Warn("indexer: the limits watch ended; window counts now follow the reprobe tick")
			}
		}()
		return nil
	})
}

// sessionsSource enqueues the Indexer whose session the index agent dropped:
// a Delete or Purge of its key in clustarr-indexer-sessions, which a cached
// client's failed relogin writes (clients.SessionStore.Drop). The next login
// is then this reconcile's rather than the next reprobe's (§5.12). A Put (a
// Save from this reconciler or an agent's successful relogin) needs no
// login and wakes nothing. Like limitsSource, a watch that ends leaves the
// 15-minute reprobe in charge, and it says so.
func (r *Reconciler) sessionsSource() source.Source {
	return source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		ch, err := r.Bus.KV(events.BucketIndexerSessions).Watch(ctx, ">")
		if err != nil {
			return fmt.Errorf("indexer: watch %s: %w", events.BucketIndexerSessions, err)
		}
		go func() {
			for e := range ch {
				if e.Operation != events.KVDelete && e.Operation != events.KVPurge {
					continue
				}
				for _, req := range r.indexersForSession(ctx, e.Key) {
					q.Add(req)
				}
			}
			if ctx.Err() == nil {
				logging.FromContext(ctx).Warn("indexer: the sessions watch ended; a dropped session now waits for the reprobe tick")
			}
		}()
		return nil
	})
}

// indexersForSession maps a clustarr-indexer-sessions key, clients.SessionKey
// of an Indexer's UID, onto that Indexer, from the manager's cache.
func (r *Reconciler) indexersForSession(ctx context.Context, key string) []reconcile.Request {
	var list indexv1alpha1.IndexerList
	if err := r.Client.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Warn("indexer: listing Indexers for a session change failed", "error", err)
		return nil
	}
	for i := range list.Items {
		if idxclients.SessionKey(list.Items[i].UID) == key {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])}}
		}
	}
	return nil
}

// indexersForRing maps a ring key -- events.KVKeyToken(uid) plus ".query" or
// ".grab" -- onto the Indexer with that UID, from the manager's cache.
func (r *Reconciler) indexersForRing(ctx context.Context, key string) []reconcile.Request {
	dot := strings.LastIndexByte(key, '.')
	if dot <= 0 {
		return nil
	}
	token := key[:dot]
	var list indexv1alpha1.IndexerList
	if err := r.Client.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Warn("indexer: listing Indexers for a ring change failed", "error", err)
		return nil
	}
	for i := range list.Items {
		if events.KVKeyToken(string(list.Items[i].UID)) == token {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])}}
		}
	}
	return nil
}

// indexersForProxy maps an IndexerProxy onto the Indexers in its namespace
// it applies to: spec.proxyRef naming it, or its spec.selector matching their
// labels. controller-runtime maps an update's old AND new object, so an
// Indexer a selector edit stops matching is reconciled too. The reconcile is
// what moves the Ready/Authenticated conditions to ProxyUnavailable (or back)
// at once; the search and poll clients notice the change on their own, from
// the proxy fingerprint the client cache keys on.
func (r *Reconciler) indexersForProxy(ctx context.Context, o client.Object) []reconcile.Request {
	p, ok := o.(*indexv1alpha1.IndexerProxy)
	if !ok {
		return nil
	}
	var list indexv1alpha1.IndexerList
	if err := r.Client.List(ctx, &list, client.InNamespace(p.Namespace)); err != nil {
		logging.FromContext(ctx).Warn("indexer: listing Indexers for a proxy change failed",
			"proxy", client.ObjectKeyFromObject(p), "error", err)
		return nil
	}
	sel, err := metav1.LabelSelectorAsSelector(&p.Spec.Selector)
	selects := err == nil && (len(p.Spec.Selector.MatchLabels) > 0 || len(p.Spec.Selector.MatchExpressions) > 0)
	var out []reconcile.Request
	for i := range list.Items {
		idx := &list.Items[i]
		named := idx.Spec.ProxyRef != nil && *idx.Spec.ProxyRef == p.Name
		// An unparseable selector could have been meant for any Indexer
		// here, and app/indexer/proxy fails every one of them closed, so every
		// one is reconciled to report it.
		if named || err != nil || (selects && sel.Matches(labels.Set(idx.Labels))) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(idx)})
		}
	}
	return out
}

// definitionIDs is the IndexerDefinition status fields spec.definition
// resolves by -- status.id and status.replaces -- as one comparable value,
// for the watch predicate. Both are written by the IndexerDefinition
// controller's status apply, which bumps no generation.
func definitionIDs(o client.Object) string {
	d, ok := o.(*indexv1alpha1.IndexerDefinition)
	if !ok {
		return ""
	}
	return strings.Join(append([]string{d.Status.ID}, d.Status.Replaces...), "\x00")
}

// indexersForDefinition maps an IndexerDefinition onto every Indexer that
// resolves through it: spec.definitionRef naming it, or spec.definition
// naming an id it provides (spec.replaces, status.id or status.replaces, the
// three keys definitionByID resolves by). IndexerDefinition is cluster-scoped and an
// Indexer in any namespace may name it, so the list is cluster-wide -- from
// the manager's cache, not the apiserver.
func (r *Reconciler) indexersForDefinition(ctx context.Context, o client.Object) []reconcile.Request {
	d, ok := o.(*indexv1alpha1.IndexerDefinition)
	if !ok {
		return nil
	}
	var list indexv1alpha1.IndexerList
	if err := r.Client.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Warn("indexer: listing Indexers for a definition change failed",
			"definition", d.Name, "error", err)
		return nil
	}
	ids := map[string]bool{}
	if d.Spec.Replaces != nil && *d.Spec.Replaces != "" {
		ids[*d.Spec.Replaces] = true
	}
	if d.Status.ID != "" {
		ids[d.Status.ID] = true
	}
	for _, old := range d.Status.Replaces {
		ids[old] = true
	}
	var out []reconcile.Request
	for i := range list.Items {
		spec := list.Items[i].Spec
		switch {
		case spec.DefinitionRef != nil && *spec.DefinitionRef == d.Name,
			spec.Definition != nil && ids[*spec.Definition]:
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return out
}
