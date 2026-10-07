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

package remediation

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mediafilestatus"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
)

// Defaults of the manager's --remediation-* flags (F3.4 binds them).
const (
	DefaultConcurrency         = 16
	DefaultBulkWritesPerSecond = 25
	DefaultBulkBurst           = 50
	DefaultIOWorkers           = 8
)

// Env is what Gather may read and what effects write through. Reader is the
// manager's cache; planners hold no writer.
type Env struct {
	Reader    client.Reader
	APIReader client.Reader
	Bus       events.Bus
	IO        *IOExecutor
	Pacer     *records.Pacer
	DataDir   string
}

// Config is the loop's tuning (--remediation-*).
type Config struct {
	Concurrency         int
	BulkWritesPerSecond int // 0 disables the status-write limiter
	BulkBurst           int
}

// Actuator runs after the effects against the applied status: replay, then
// rename (§3.9). It writes through c; wake asks for a pass that soon.
type Actuator interface {
	Name() string
	Act(ctx context.Context, env *Env, c client.Client, applied *catalogv1alpha1.MediaFile) (wake time.Duration, err error)
}

// actuatorOrder is §3.4 step 10's.
var actuatorOrder = []string{"replay", "rename"}

// Reconciler is the remediation loop.
type Reconciler struct {
	// Clock is the pass's clock; NewReconciler sets time.Now.
	Clock func() time.Time
	// Items is the item path (loop spec §3.12): the loop's second key type.
	// app/remediation/manager.Register sets it before SetupWithManager.
	Items    ItemReconciler
	c        client.Client
	env      *Env
	recorder k8sevents.EventRecorder
	cfg      Config
	planners []Bound
	acts     []Actuator
	limiter  *WriteLimiter
	keys     keyStates
}

// NewReconciler binds the planners in Order and the actuators in their order.
func NewReconciler(c client.Client, env *Env, rec k8sevents.EventRecorder, cfg Config, planners []Bound, actuators ...Actuator) (*Reconciler, error) {
	seen := map[PlannerName]bool{}
	for _, p := range planners {
		if seen[p.Name()] {
			return nil, fmt.Errorf("remediation: planner %q is bound twice", p.Name())
		}
		seen[p.Name()] = true
		if !slices.Contains(Order, p.Name()) && p.Name() != PlannerAdopt {
			return nil, fmt.Errorf("remediation: planner %q is not in remediation.Order", p.Name())
		}
	}
	sorted := slices.Clone(planners)
	slices.SortStableFunc(sorted, func(a, b Bound) int { return orderIndex(a.Name()) - orderIndex(b.Name()) })
	acts := slices.Clone(actuators)
	slices.SortStableFunc(acts, func(a, b Actuator) int {
		return slices.Index(actuatorOrder, a.Name()) - slices.Index(actuatorOrder, b.Name())
	})
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.BulkBurst <= 0 {
		cfg.BulkBurst = DefaultBulkBurst
	}
	return &Reconciler{
		Clock: time.Now, c: c, env: env, recorder: rec, cfg: cfg, planners: sorted, acts: acts,
		limiter: NewWriteLimiter(cfg.BulkWritesPerSecond, cfg.BulkBurst),
	}, nil
}

func orderIndex(n PlannerName) int {
	if n == PlannerAdopt {
		return -1
	}
	return slices.Index(Order, n)
}

// PlannerNames is the bound planners' names in the order a pass runs them.
func (r *Reconciler) PlannerNames() []PlannerName {
	out := make([]PlannerName, 0, len(r.planners))
	for _, p := range r.planners {
		out = append(out, p.Name())
	}
	return out
}

// Reconcile runs one key's pass: a file key's (§3.4), or an item key's
// through Items (§3.12).
func (r *Reconciler) Reconcile(ctx context.Context, k Key) (reconcile.Result, error) {
	if k.Kind != KindMediaFile {
		return r.Items.Reconcile(ctx, k)
	}
	return r.reconcileFile(ctx, k.NamespacedName())
}

// Pass outcomes, clustarr_remediation_passes_total's outcome label.
const (
	passApplied   = "applied"
	passUnchanged = "unchanged"
	passConflict  = "conflict"
	passStale     = "stale"
	passPaced     = "paced"
	passError     = "error"
)

func (r *Reconciler) count(outcome string, res reconcile.Result) reconcile.Result {
	metrics.RemediationPassesTotal.WithLabelValues(string(KindMediaFile), outcome).Inc()
	return res
}

// reconcileFile is one file's pass (§3.4).
func (r *Reconciler) reconcileFile(ctx context.Context, nn types.NamespacedName) (reconcile.Result, error) {
	ctx, span := tracing.Start(ctx, "remediation.Reconcile", trace.WithAttributes(
		attribute.String("key.kind", string(KindMediaFile)),
		attribute.String("key.namespace", nn.Namespace), attribute.String("key.name", nn.Name)))
	defer span.End()
	ctx = logging.With(ctx, "mediafile", nn.Name, "namespace", nn.Namespace)
	ks := r.keys.get(nn)

	// 1. Load.
	var mf catalogv1alpha1.MediaFile
	if err := r.env.Reader.Get(ctx, nn, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			r.keys.forget(nn)
			return reconcile.Result{}, nil
		}
		return r.count(passError, reconcile.Result{RequeueAfter: ks.transientBackoff(), Priority: new(0)}), nil
	}
	if k8s.IsDeleting(&mf) {
		return reconcile.Result{}, nil
	}

	// 2. View.
	now := metav1.NewTime(r.Clock().UTC().Truncate(time.Second))
	v := &View{File: mf.DeepCopy(), Now: now, seqFloor: mf.Status.LastSeq}
	v.Prev = &v.File.Status
	v.Stale = ks.stale(mf.UID, v.Prev, now.Time)
	v.Draft = v.Prev.DeepCopy()

	// 3. Gather. A planner after the probe gathers nothing while the stored
	// probe is pending: it would read a summary of bytes that are gone
	// (§3.5's structural gate; W4.14's replacement).
	gated := r.gate()
	prevCurrent := (&catalogv1alpha1.MediaFile{Status: *v.Prev}).ProbeCurrent()
	ins := make([]any, len(r.planners))
	fails := make([]outcome, len(r.planners))
	skipped := make([]bool, len(r.planners))
	for i, p := range r.planners {
		if !p.Applies(v.File) {
			continue
		}
		if gated[i] && !prevCurrent {
			skipped[i] = true
			continue
		}
		fails[i] = r.isolate(ctx, p.Name(), "gather", gatherTimeout(p.Name()), func(ctx context.Context) error {
			in, err := p.gather(ctx, r.env, v)
			ins[i] = in
			return err
		})
	}

	// 4. Plan, in Order, each over the draft the planners before it left.
	results := make([]Result, len(r.planners))
	for i, p := range r.planners {
		next := v.Draft.DeepCopy()
		switch {
		case gated[i] && !v.ProbeCurrent():
			p.Copy(v.Prev, next) // §3.5: its last block, while the probe is pending
		case gated[i] && skipped[i]:
			p.Copy(v.Prev, next) // the probe landed this pass; its Gather did not run
			results[i] = Result{Again: true}
		case !p.Applies(v.File):
			p.Copy(&catalogv1alpha1.MediaFileStatus{}, next)
		case fails[i].failed():
			p.Copy(v.Prev, next)
		default:
			out := v.Draft.DeepCopy()
			fails[i] = r.isolate(ctx, p.Name(), "plan", 0, func(context.Context) error {
				var err error
				results[i], err = p.plan(v, ins[i], out)
				return err
			})
			if fails[i].failed() {
				results[i] = Result{}
				p.Copy(v.Prev, next)
			} else {
				p.Copy(out, next)
			}
		}
		v.Draft = next
		if p.Name() == PlannerProbe {
			v.Main = results[i].Main
		}
	}

	// 5. Loop-owned fields.
	draft := v.Draft
	draft.ObservedGeneration = mf.Generation
	draft.LastSeq = v.seqFloor
	r.markPlannerConditions(&mf, &draft.Conditions, fails, ks)
	k8s.MarkDeadLettered(&mf, &draft.Conditions)                  // §2.8: the loop's own condition
	renderedValue, drops := mediafilestatus.Render(v.Prev, draft) // F2.2: clamp, C1, a stored value never re-clamped
	rendered := &renderedValue

	// 6. Main resource.
	viewRV, viewGen := mf.ResourceVersion, mf.Generation
	if mac, changed := renderMain(&mf, rendered, v.Main); changed {
		applied, err := k8s.Apply(ctx, r.c, k8s.ManagerCatalogarr, mac.WithResourceVersion(mf.ResourceVersion))
		switch {
		case apierrors.IsConflict(err):
			return r.count(passConflict, reconcile.Result{RequeueAfter: ks.conflictBackoff(), Priority: new(0)}), nil
		case err != nil && IsTransient(err):
			return r.count(passError, reconcile.Result{RequeueAfter: ks.transientBackoff(), Priority: new(0)}), nil
		case err != nil:
			return reconcile.Result{}, fmt.Errorf("remediation: main apply: %w", err)
		}
		if applied.ObjectMetaApplyConfiguration != nil {
			viewRV, viewGen = ptr.Deref(applied.ResourceVersion, viewRV), ptr.Deref(applied.Generation, viewGen)
		}
		rendered.ObservedGeneration = viewGen // the loop's own spec change is observed
	}

	// 7. Status: one compare-and-swap at the resourceVersion the pass
	// planned from (or its own main apply returned).
	changed := !equality.Semantic.DeepEqual(normalize(rendered), normalize(v.Prev))
	if changed && !anyUnpaced(results) {
		if wait := r.limiter.Admit(mf.UID, now.Time); wait > 0 {
			return r.count(passPaced, reconcile.Result{RequeueAfter: wait, Priority: new(PriorityTimed)}), nil
		}
	}
	outcomeLabel := passUnchanged
	appliedStatus := v.Prev
	if changed {
		_, ok, err := k8s.PatchStatusCAS(ctx, viewReader{obj: v.File, rv: viewRV, gen: viewGen}, r.c, k8s.ManagerCatalogarr,
			nn, newMediaFile, func(fresh *catalogv1alpha1.MediaFile) (*catalogac.MediaFileApplyConfiguration, bool, error) {
				if equality.Semantic.DeepEqual(normalize(rendered), normalize(&fresh.Status)) {
					return nil, true, nil
				}
				ac, err := statusApply(fresh.Name, fresh.Namespace, rendered)
				return ac, false, err
			}, 1)
		switch {
		case apierrors.IsConflict(err):
			return r.count(passConflict, reconcile.Result{RequeueAfter: ks.conflictBackoff(), Priority: new(0)}), nil
		case err != nil && IsTransient(err):
			return r.count(passError, reconcile.Result{RequeueAfter: ks.transientBackoff(), Priority: new(0)}), nil
		case err != nil:
			return reconcile.Result{}, fmt.Errorf("remediation: status apply: %w", err)
		}
		if ok {
			ks.remember(mf.UID, rendered, now.Time)
			appliedStatus, outcomeLabel = rendered, passApplied
			r.countRecords(results)
			r.emitDrops(&mf, drops)
		}
	} else if v.Stale {
		// The cache has not caught up with this process's last apply: a
		// quiet pass now would end on a view that is behind (§3.7).
		return r.count(passStale, reconcile.Result{RequeueAfter: ks.conflictBackoff(), Priority: new(0)}), nil
	}

	// 8. Effects, in Order, after the status that owes them landed.
	var casMissed, transient bool
	for i, p := range r.planners {
		if len(results[i].Effects) == 0 {
			continue
		}
		eo := r.runEffects(ctx, string(mf.UID), p, results[i].Effects)
		casMissed = casMissed || eo.casMiss
		transient = transient || eo.transient
		if eo.failed.failed() {
			ks.effectFailures[p.Name()] = eo.failed
		} else {
			delete(ks.effectFailures, p.Name())
		}
	}
	r.emitEvents(&mf, appliedStatus, results)

	// 9. Actuators, against the applied status.
	appliedFile := v.File.DeepCopy()
	appliedFile.Status = *appliedStatus
	if v.Main != nil && v.Main.Path != "" {
		appliedFile.Spec.Path, appliedFile.Spec.SizeBytes, appliedFile.Spec.ModTime = v.Main.Path, v.Main.SizeBytes, v.Main.ModTime
	}
	appliedFile.ResourceVersion, appliedFile.Generation = viewRV, viewGen
	var wakes []time.Duration
	for _, a := range r.acts {
		wake, err := a.Act(ctx, r.env, r.c, appliedFile)
		if err != nil {
			if !IsTransient(err) {
				logging.FromContext(ctx).Warn("remediation: actuator failed", "actuator", a.Name(), "error", err)
			}
			transient = true
		}
		if wake > 0 {
			wakes = append(wakes, wake)
		}
	}

	// 10. Result.
	res := r.result(ks, now.Time, results, fails, wakes, casMissed, transient)
	if !casMissed && !transient && !slices.ContainsFunc(fails, outcome.failed) {
		ks.clean()
	}
	return r.count(outcomeLabel, res), nil
}

// result is §3.11: the earliest Due and actuator wake, floored at 1 s;
// Again or a CAS miss in 1 s at priority 0; a transient fault on the per-key
// backoff; a planner error or panic after plannerFailedRetry.
func (r *Reconciler) result(ks *keyState, now time.Time, results []Result, fails []outcome, wakes []time.Duration,
	casMissed, transient bool,
) reconcile.Result {
	var after time.Duration
	shorten := func(d time.Duration) {
		if after == 0 || d < after {
			after = d
		}
	}
	for _, res := range results {
		if !res.Due.IsZero() {
			shorten(max(res.Due.Sub(now), time.Second))
		}
	}
	for _, w := range wakes {
		shorten(w)
	}
	prio := PriorityTimed
	if anyAgain(results) || casMissed || slices.ContainsFunc(fails, outcome.casMiss) {
		shorten(time.Second)
		prio = 0
	}
	if transient || slices.ContainsFunc(fails, outcome.transient) {
		shorten(ks.transientBackoff())
	}
	if anyFailed(fails) || len(ks.effectFailures) > 0 {
		shorten(plannerFailedRetry)
	}
	if after == 0 {
		return reconcile.Result{}
	}
	return reconcile.Result{RequeueAfter: after, Priority: new(prio)}
}

// markPlannerConditions sets each planner's <Planner>PlannerError from its
// isolation outcome (an error or panic sets it, success removes it, a
// transient fault or a CAS miss leaves it as it stands) or, failing that,
// from an effect failure remembered from the last pass.
func (r *Reconciler) markPlannerConditions(mf *catalogv1alpha1.MediaFile, conds *[]metav1.Condition, fails []outcome, ks *keyState) {
	for i, p := range r.planners {
		typ := conditionFor(p.Name())
		if typ == "" {
			continue
		}
		o := fails[i]
		if !o.failed() {
			if ef, ok := ks.effectFailures[p.Name()]; ok {
				o = ef
			}
		}
		switch {
		case o.transient(), o.casMiss():
		case o.failed():
			reason := catalogv1alpha1.MediaFileReasonPlannerError
			if o.reason == failPanic {
				reason = catalogv1alpha1.MediaFileReasonPlannerPanic
			}
			k8s.MarkTrue(mf, conds, typ, reason, "%s", k8s.ClampText(o.message, k8s.MaxConditionMessage))
		default:
			k8s.RemoveCondition(conds, typ)
		}
	}
}

// emitEvents records each planner's Events on the file (recorder
// "mediafile", action the planner's name unless the Event names one).
func (r *Reconciler) emitEvents(mf *catalogv1alpha1.MediaFile, _ *catalogv1alpha1.MediaFileStatus, results []Result) {
	if r.recorder == nil {
		return
	}
	for i, res := range results {
		for _, e := range res.Events {
			action := e.Action
			if action == "" {
				action = string(r.planners[i].Name())
			}
			r.recorder.Eventf(mf, nil, e.Type, e.Reason, action, "%s", e.Message)
		}
	}
}

// emitDrops records one Warning Event per entry Render dropped (F2.2: a path
// or name over its cap is dropped, never truncated).
func (r *Reconciler) emitDrops(mf *catalogv1alpha1.MediaFile, drops []mediafilestatus.Drop) {
	if r.recorder == nil {
		return
	}
	for _, d := range drops {
		r.recorder.Eventf(mf, nil, corev1.EventTypeWarning, mediafilestatus.EventReasonEntryDropped, "render",
			"%s dropped: %s (%d bytes)", d.Field, d.Reason, d.Bytes)
	}
}

// countRecords counts each planner's record notes once its apply landed.
func (r *Reconciler) countRecords(results []Result) {
	for i, res := range results {
		for _, n := range res.Records {
			if n.TimedOut {
				metrics.RecordTimeoutsTotal.WithLabelValues(string(r.planners[i].Name())).Inc()
				continue
			}
			metrics.RecordIncorporationsTotal.WithLabelValues(string(r.planners[i].Name()), n.State).Inc()
		}
	}
}

func anyUnpaced(rs []Result) bool {
	return slices.ContainsFunc(rs, func(r Result) bool { return r.Unpaced })
}

func anyAgain(rs []Result) bool {
	return slices.ContainsFunc(rs, func(r Result) bool { return r.Again })
}

// anyFailed reports an error or a panic: a transient fault and a CAS miss
// set no condition and retry on their own timers.
func anyFailed(os []outcome) bool {
	return slices.ContainsFunc(os, func(o outcome) bool { return o.failed() && !o.transient() && !o.casMiss() })
}

// gate marks every planner after the probe planner in Order, when one is
// bound (§3.5's structural gate; TestNoPlannerPlansFromAPendingProbe).
func (r *Reconciler) gate() []bool {
	out := make([]bool, len(r.planners))
	probe := slices.IndexFunc(r.planners, func(p Bound) bool { return p.Name() == PlannerProbe })
	if probe < 0 {
		return out
	}
	for i := probe + 1; i < len(r.planners); i++ {
		out[i] = true
	}
	return out
}

// SetupWithManager registers the loop as controller "mediafile": S1, then
// every source a bound planner or actuator declares, deduplicated by name,
// then the item path's sources (the item arm of the MediaFile source and
// each item kind's Watches, §3.12).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := builder.TypedControllerManagedBy[Key](mgr).Named(ControllerName).
		Watches(&catalogv1alpha1.MediaFile{}, mediaFileEvents())
	seen := map[string]bool{}
	var watchers []Watcher
	for _, p := range r.planners {
		if w, ok := p.unwrap().(Watcher); ok {
			watchers = append(watchers, w)
		}
	}
	for _, a := range r.acts {
		if w, ok := a.(Watcher); ok {
			watchers = append(watchers, w)
		}
	}
	for _, w := range watchers {
		srcs, err := w.Sources(mgr)
		if err != nil {
			return err
		}
		for _, s := range srcs {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			if s.Raw != nil {
				b = b.WatchesRawSource(s.Raw)
				continue
			}
			b = b.Watches(s.Object, s.Handler, builder.WithPredicates(s.Predicates...))
		}
	}
	b = r.Items.Watch(b)
	return b.WithOptions(controller.TypedOptions[Key]{
		MaxConcurrentReconciles: r.cfg.Concurrency,
		RecoverPanic:            new(true),
		ReconciliationTimeout:   5 * time.Minute,
	}).Complete(r)
}
