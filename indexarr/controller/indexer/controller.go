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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

const (
	// reprobeInterval is the steady-state tick. It is short because the
	// RateLimited and Healthy conditions are derived from worker-owned
	// status fields that k8s.GenerationChanged() deliberately filters out
	// of the watch, so this tick is the only thing that refreshes them.
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

	// Limiters paces every outbound indexer request in this process, keyed
	// by indexer host. D1-8 constructs exactly one and hands the same
	// instance to this reconciler, to the search fan-out and to the RSS
	// worker, so all three share one bucket per host.
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

	// Clients is the process-wide [ClientCache] the search fan-out and the
	// RSS poll read through. This reconciler does not BUILD clients with it
	// -- it has the fresh spec and the fresh Secret in hand and calls
	// buildClient directly -- it only evicts, so a deleted Indexer does not
	// leave its client (and that client's idle connections) behind.
	//
	// Optional: a nil Clients evicts nothing, which is correct for a unit
	// test and merely wasteful in a process that somehow had no cache.
	Clients *ClientCache

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
	Sessions *SessionStore

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
// mgr.GetEventRecorder and writes events.k8s.io/v1 Events; limiters is the one
// process-wide *ratelimit.Limiter indexarr shares across the caps probe, the
// search fan-out and the RSS poll; bus is the one the RSS worker consumes
// from, and is what lets this reconciler seed the first poll.
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
		Sessions: NewSessionStore(c, bus),
		capsSeen: map[types.UID]capsMemo{},
	}
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
	now := time.Now()

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
		// The caps memo and the client cache are both keyed by UID and are
		// safe to prune. The limiter bucket is keyed by HOST and is not --
		// see the Limiters field's comment.
		r.forget(idx.UID)
		if r.Clients != nil {
			r.Clients.Forget(idx.UID)
		}
		return ctrl.Result{}, nil
	}

	conditions := append([]metav1.Condition(nil), idx.Status.Conditions...)

	// What this pass resolves is written onto the LOCAL copy of the status
	// first, and indexarr/status.ControllerFields then seeds the apply from
	// that copy. The apply is therefore a complete declaration of the
	// indexarr-owned set by construction, on every return path, rather than
	// by a discipline remembered at seven call sites -- so an early return
	// on a transient failure re-sends the caps, protocol and privacy that
	// are already there instead of releasing them.
	idx.Status.ObservedGeneration = idx.Generation
	idx.Status.SessionSecretRef = sessionSecretName(idx.Name)

	kind, err := resolveSource(idx.Spec)
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

	if kind != sourceGeneric {
		return r.reconcileDefinition(ctx, &idx, conditions, now)
	}

	secret, err := readSecret(ctx, r.Client, idx.Namespace, idx.Spec.SecretRef)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkUnknown(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonDependencyNotReady, "%s", err.Error())
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
		return r.patch(ctx, &idx, conditions, ctrl.Result{RequeueAfter: reprobeInterval})
	}
	idx.Status.Protocol = protocolFor(kind, idx.Spec)
	idx.Status.Privacy = privacyFor(kind, secret)

	// The bucket config for this host, written here and nowhere else: this
	// reconciler is the only reader of spec.requestDelay, and the search
	// fan-out, the RSS poll and the download verb only Wait on the same
	// *ratelimit.Limiter. It used to live inside buildClient, which
	// ClientCache now shares -- see applyRateLimit.
	applyRateLimit(idx.Spec, r.Limiters, 0)

	transport, err := resolveProxy(ctx, r.Client, &idx)
	if err != nil {
		return r.proxyUnavailable(ctx, &idx, conditions, err)
	}

	tc, endpoint, err := buildClient(idx.Spec, secret, r.Limiters, transport)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkReady(&idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		if _, perr := r.patch(ctx, &idx, conditions, ctrl.Result{}); perr != nil {
			return ctrl.Result{}, perr
		}
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	outcome := probeOutcome{}
	probed := false
	if r.shouldProbe(idx.UID, idx.Generation, idx.Status.Caps != nil, now) {
		probed = true
		start := time.Now()
		wire, perr := tc.Caps(ctx)
		// The `function` label is the Torznab t= value, and t=caps is not
		// a SearchMode -- hence the literal. `indexer` is the OBJECT NAME:
		// never a title, a host or an indexer-supplied string.
		metrics.IndexerQueryDuration.WithLabelValues(idx.Name, "caps").Observe(time.Since(start).Seconds())
		outcome = classify(perr)
		metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, outcomeLabel(outcome)).Inc()
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

	return r.finish(ctx, &idx, conditions, probed, outcome, time.Now())
}

// finish derives Authenticated, RateLimited, Healthy and Ready from what this
// pass learned, seeds the RSS chain and applies. It is the shared tail of the
// generic and the definition-backed paths, so the two cannot disagree about
// what a failed probe or a failed login means for Ready.
func (r *Reconciler) finish(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	conditions []metav1.Condition,
	probed bool,
	outcome probeOutcome,
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
		k8s.MarkTrue(idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, ReasonLimitReached, "%s", limitMsg)
	} else {
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, k8s.ReasonReconciled, "under the configured limits")
	}

	healthy := idxstatus.Healthy(idx.Status, now) && outcome.Reason == ""
	switch {
	case !idxstatus.Healthy(idx.Status, now):
		k8s.MarkFalse(idx, &conditions, indexv1alpha1.IndexerConditionHealthy, ReasonBackingOff,
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
		k8s.MarkReady(idx, &conditions, false, ReasonProbeFailed, "caps have never been probed successfully")
	default:
		k8s.MarkReady(idx, &conditions, false,
			firstNonEmpty(outcome.Reason, ReasonBackingOff),
			"%s", firstNonEmpty(outcome.Message, "backing off"))
	}

	// Seed BEFORE the patch, but do not return on its error: the status
	// write is unconditional on every path through Reconcile, and an early
	// return here would be exactly the partial-status bug this package is
	// built to avoid. The bus failure is surfaced after the write instead.
	seedErr := r.seedRSSSchedule(ctx, idx, healthy, now)

	res, err := r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: r.requeueAfter(idx.Status, outcome, now)})
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
// restart. rss.NextPollAt returns the slot the worker's own reschedule
// already chose, rss.TaskMsgID quantises it to the second, and
// CLUSTARR_WORK_INDEXARR deduplicates for an hour (pkg/events/topology.go's
// work() helper, Duplicates: time.Hour), so the duplicate seed stores
// nothing. Where it does store something -- a slot past the dedup window, or
// a genuinely new slot -- the scheduled publish carries Nats-Rollup: sub, so
// it REPLACES the pending poll rather than adding a second one.
func (r *Reconciler) seedRSSSchedule(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	healthy bool,
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
	return rss.ScheduleNext(ctx, r.Bus, idx, rss.NextPollAt(idx, now))
}

// rateLimited derives the RateLimited condition from spec.limits and the
// worker-owned counters status.queriesInWindow / status.grabsInWindow. Note
// Prowlarr counts IndexerQuery + IndexerRss together against QueryLimit; both
// land in queriesInWindow, which the RSS worker and the search fan-out
// increment.
func rateLimited(spec indexv1alpha1.IndexerSpec, st indexv1alpha1.IndexerStatus) (bool, string) {
	if spec.Limits == nil {
		return false, ""
	}
	unit := spec.Limits.Unit
	if unit == "" {
		unit = indexv1alpha1.LimitUnitDay
	}
	if l := spec.Limits.QueryLimit; l != nil && st.QueriesInWindow >= *l {
		return true, fmt.Sprintf("queries %d/%d per %s", st.QueriesInWindow, *l, unit)
	}
	if l := spec.Limits.GrabLimit; l != nil && st.GrabsInWindow >= *l {
		return true, fmt.Sprintf("grabs %d/%d per %s", st.GrabsInWindow, *l, unit)
	}
	return false, ""
}

// requeueAfter picks the next tick. It never returns 0, because a zero
// RequeueAfter means "do not requeue" and this reconciler's RateLimited and
// Healthy conditions are derived from worker-owned fields the watch predicate
// filters out -- only the tick refreshes them. It is always RequeueAfter,
// never a bare Requeue and never an error returned purely to force a retry.
func (r *Reconciler) requeueAfter(st indexv1alpha1.IndexerStatus, outcome probeOutcome, now time.Time) time.Duration {
	// A server's Retry-After is authoritative; pkg/ratelimit.Backoff
	// documents the same rule for the same reason.
	if outcome.RetryAfter > 0 {
		return outcome.RetryAfter
	}
	if st.DisabledUntil != nil && now.Before(st.DisabledUntil.Time) {
		return st.DisabledUntil.Sub(now) + time.Second
	}
	return reprobeInterval
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
// It routes through indexarr/status.Patch (ruling R31), which holds the one
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
// spec edits, and a change of the status.id that spec.definition resolves
// by; [indexersForDefinition] maps each onto the Indexers that name it.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("indexer").
		For(&indexv1alpha1.Indexer{}, builder.WithPredicates(
			k8s.Or(k8s.GenerationChanged(), k8s.DeadLetteredAnnotationChanged()))).
		Watches(&indexv1alpha1.IndexerDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.indexersForDefinition),
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(),
				k8s.StatusFieldChanged(definitionID)))).
		WithOptions(controller.Options{
			ReconciliationTimeout: 5 * time.Minute,
			RecoverPanic:          ptr.To(true),
		}).
		Complete(r)
}

// definitionID is the IndexerDefinition status field spec.definition
// resolves by, for the watch predicate.
func definitionID(o client.Object) string {
	d, ok := o.(*indexv1alpha1.IndexerDefinition)
	if !ok {
		return ""
	}
	return d.Status.ID
}

// indexersForDefinition maps an IndexerDefinition onto every Indexer that
// resolves through it: spec.definitionRef naming it, or spec.definition
// naming the id it provides (spec.replaces or status.id, the two keys
// definitionByID resolves by). IndexerDefinition is cluster-scoped and an
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
