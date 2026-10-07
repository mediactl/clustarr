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
	"maps"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	cataloghistory "github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ItemReconciler is the loop's item path (loop spec §3.12): one rollup.Item
// per item kind, reconciled by its own ReconcileItem, woken by its own
// non-file watches adapted to Key and by the item arm of the MediaFile
// source (S1, S1'), which replaced every item kind's MediaFile watch.
//
// Since ADR-0019 (A3.3, ruling R5) a pass also runs the item stages around
// ReconcileItem: each stage plans beside the rollups, its catalogarr
// contribution rides the context into the kind's one catalogarr apply
// (itempass), and every other manager's set is applied here, chained on
// the resourceVersion the apply before it returned (§7.0); then the stages'
// effects run, then their Events.
type ItemReconciler struct {
	Items  map[KeyKind]rollup.Item
	Stages []ItemStage
	Env    *Env
	// Dispatch is the admission ledger DispatchPublish and DispatchAnswered
	// confirm; Book the delivery book planners render from.
	Dispatch *dispatch.Ledger
	Book     *dispatch.DeliveryBook
	// Inbox is the candidate inbox: S30's wakes and SettleCandidate.
	Inbox *intake.Inbox
	// DeliveryWakes are the advisory intake's wakes (a resolved nak or term).
	DeliveryWakes <-chan cataloghistory.Target
	// Client writes the item's finalizer, the other managers' sets and the
	// effects' objects.
	Client client.Client
	// Recorder is mgr.GetEventRecorder: an ItemEvent's recorder by name.
	Recorder func(name string) k8sevents.EventRecorder
	// SpecWriter applies a placed file's MediaFile spec (A3.8).
	SpecWriter SpecWriter
	// Clock is the pass's clock; nil is time.Now.
	Clock func() time.Time
}

// itemEffectRetry is when a pass whose item effect failed transiently runs
// again.
const itemEffectRetry = 5 * time.Second

// Reconcile runs k's item path. A timer the item asks for (an air date, an
// availability date, a conflict requeue) is a Due requeue, at PriorityTimed
// (§3.11). The pass is spanned and counted as a file pass is (§3.19); an
// item apply is never skipped (§3.12), so a pass that returns no error
// counts as applied.
func (ir *ItemReconciler) Reconcile(ctx context.Context, k Key) (reconcile.Result, error) {
	it, ok := ir.Items[k.Kind]
	if !ok {
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("remediation: no item path for %s %s/%s", k.Kind, k.Namespace, k.Name))
	}
	ctx, span := tracing.Start(ctx, "remediation.Reconcile", trace.WithAttributes(
		attribute.String("key.kind", string(k.Kind)),
		attribute.String("key.namespace", k.Namespace), attribute.String("key.name", k.Name)))
	defer span.End()
	var (
		res reconcile.Result
		err error
	)
	if len(ir.Stages) == 0 || ir.Env == nil {
		res, err = it.ReconcileItem(ctx, types.NamespacedName{Namespace: k.Namespace, Name: k.Name})
	} else {
		res, err = ir.staged(ctx, it, k)
	}
	outcome := passApplied
	if err != nil {
		outcome = passError
	}
	metrics.RemediationPassesTotal.WithLabelValues(string(k.Kind), outcome).Inc()
	if res.RequeueAfter > 0 && res.Priority == nil {
		res.Priority = new(PriorityTimed)
	}
	return res, err
}

// staged is one item pass with its stages (A3.3 step 3).
func (ir *ItemReconciler) staged(ctx context.Context, it rollup.Item, k Key) (reconcile.Result, error) {
	nn := types.NamespacedName{Namespace: k.Namespace, Name: k.Name}
	clock := ir.Clock
	if clock == nil {
		clock = time.Now
	}

	// 1. Load. A key NotFound in the cache is decided on the APIReader's
	// answer only when a stage holds evidence for it (§6.7).
	item, ok := newItemObject(k.Kind)
	if !ok {
		return it.ReconcileItem(ctx, nn)
	}
	if err := ir.Env.Reader.Get(ctx, nn, item); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{RequeueAfter: itemEffectRetry, Priority: new(0)}, nil
		}
		return ir.ownerGone(ctx, k)
	}

	// 2. Stages, in StageOrder, each in isolation.
	v := &ItemView{Key: k, Item: item.DeepCopyObject().(client.Object), Now: metav1.NewTime(clock().UTC().Truncate(time.Second))}
	var (
		merged         itempass.Contribution
		sets           = map[k8s.FieldManager]itempass.Set{}
		results        = map[ItemStageName]ItemResult{}
		failed         bool
		stageTransient bool
		order          []ItemStageName
	)
	for _, name := range StageOrder {
		for _, s := range ir.Stages {
			if s.Name() != name || !s.Applies(k.Kind) {
				continue
			}
			so := runStage(ctx, ir.Env, s, v)
			if so.failed.failed() {
				if so.failed.transient() {
					stageTransient = true
				} else {
					failed = true
				}
				logging.FromContext(ctx).Warn("remediation: item stage failed; its stored values stand this pass",
					"stage", name, "reason", so.failed.reason, "error", so.failed.message)
				continue
			}
			// 3. Merge: each field from at most one stage.
			if err := mergeContribution(&merged, so.res.Contribution, name); err != nil {
				panic(err)
			}
			if err := mergeSets(sets, so.res.Sets, name); err != nil {
				panic(err)
			}
			if name == StageGrab {
				v.NewEntries = so.res.NewEntries
			}
			if so.res.Downloads != nil {
				v.Downloads = so.res.Downloads
			}
			results[name] = so.res
			order = append(order, name)
		}
	}

	// 4. The kind's own pass, with the contribution on its context. A pass
	// that did not land (a conflict, an error, a path that returned before
	// applying) stops here: no set, no effect.
	pass := &itempass.Pass{Contribution: merged}
	res, err := it.ReconcileItem(itempass.With(ctx, pass), nn)
	rv, landed := pass.LandedRV()
	if err != nil || !landed {
		return res, err
	}

	// 5. Every other manager's set, CAS-chained, skipped when equal at its
	// own paths.
	for _, s := range orderedSets(sets) {
		if itemstatus.SetEqual(item, s) {
			continue
		}
		next, conflicted, err := itemstatus.ApplySet(ctx, ir.Client, item, rv, s)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("remediation: %s set: %w", s.Manager, err)
		}
		if conflicted {
			return reconcile.Result{RequeueAfter: time.Second, Priority: new(0)}, nil
		}
		rv = next
	}

	// 6. Effects, in StageOrder, each stage's in its own order; the first
	// failure stops that stage's batch. Then the Events.
	transient, effectFailed := false, false
	for _, name := range order {
		for _, eff := range results[name].Effects {
			o := ir.isolateEffect(ctx, name, item, eff)
			if !o.failed() {
				continue
			}
			if o.transient() || o.casMiss() {
				transient = true
			} else {
				effectFailed = true
				logging.FromContext(ctx).Warn("remediation: item effect failed", "stage", name, "effect", eff.Kind(), "error", o.message)
			}
			break
		}
	}
	for _, name := range order {
		ir.emitItemEvents(item, k, results[name].Events)
	}

	// 7. Requeue at the earliest of the kind's result, every stage's Due,
	// Again, a failed effect's backoff and a failed stage's retry.
	now := v.Now.Time
	after := res.RequeueAfter
	prio := res.Priority
	shorten := func(d time.Duration, p int) {
		if after == 0 || d < after {
			after, prio = d, new(p)
		}
	}
	for _, name := range order {
		r := results[name]
		if !r.Due.IsZero() {
			shorten(max(r.Due.Sub(now), time.Second), PriorityTimed)
		}
		if r.Again {
			shorten(time.Second, 0)
		}
	}
	if transient || stageTransient {
		shorten(itemEffectRetry, 0)
	}
	if failed || effectFailed {
		shorten(plannerFailedRetry, PriorityTimed)
	}
	res.RequeueAfter, res.Priority = after, prio
	return res, nil
}

// ownerGone is a NotFound key's pass: a stage that holds evidence for it
// decides on the APIReader's answer and owes effects; otherwise nothing
// (F4.2's behaviour).
func (ir *ItemReconciler) ownerGone(ctx context.Context, k Key) (reconcile.Result, error) {
	for _, s := range ir.Stages {
		og, ok := s.(OwnerGone)
		if !ok || !og.Holds(k) {
			continue
		}
		effs, evs, err := og.Gone(ctx, ir.Env, k)
		if err != nil {
			return reconcile.Result{RequeueAfter: itemEffectRetry, Priority: new(0)}, nil
		}
		transient := false
		for _, eff := range effs {
			o := ir.isolateEffect(ctx, s.Name(), nil, eff)
			if o.failed() {
				transient = true
				break
			}
		}
		ir.emitItemEvents(nil, k, evs)
		if transient {
			return reconcile.Result{RequeueAfter: itemEffectRetry, Priority: new(0)}, nil
		}
	}
	return reconcile.Result{}, nil
}

// isolateEffect runs one item effect under recover and classifies it.
func (ir *ItemReconciler) isolateEffect(ctx context.Context, stage ItemStageName, item client.Object, eff Effect) (o outcome) {
	defer func() {
		if p := recover(); p != nil {
			o = outcome{reason: failPanic, message: fmt.Sprintf("effect panicked: %v", p)}
		}
		label := "ok"
		if o.failed() {
			label = o.reason
		}
		metrics.RemediationEffectsTotal.WithLabelValues("item."+string(stage), string(eff.Kind()), label).Inc()
	}()
	err := ir.runItemEffect(ctx, item, eff)
	switch {
	case err == nil:
		return outcome{}
	case IsCASMiss(err):
		return outcome{reason: failCASMiss, message: err.Error()}
	case IsTransient(err):
		return outcome{reason: failTransient, message: err.Error()}
	default:
		return outcome{reason: failError, message: err.Error()}
	}
}

// emitItemEvents records each stage's Events: on the event's object, else
// the item; under the event's recorder, else the kind's (its lower-cased
// name, the recorders' convention).
func (ir *ItemReconciler) emitItemEvents(item client.Object, k Key, evs []ItemEvent) {
	if ir.Recorder == nil {
		return
	}
	for _, e := range evs {
		on := e.On
		if on == nil {
			on = item
		}
		if on == nil {
			continue
		}
		name := e.Recorder
		if name == "" {
			name = strings.ToLower(string(k.Kind))
		}
		rec := ir.Recorder(name)
		if rec == nil {
			continue
		}
		action := e.Action
		if action == "" {
			action = e.Reason
		}
		rec.Eventf(on, nil, e.Type, e.Reason, action, "%s", e.Message)
	}
}

// newItemObject is an empty object of an item kind.
func newItemObject(kind KeyKind) (client.Object, bool) {
	switch kind {
	case KindMovie:
		return &catalogv1alpha1.Movie{}, true
	case KindEpisode:
		return &catalogv1alpha1.Episode{}, true
	case KindAlbum:
		return &catalogv1alpha1.Album{}, true
	case KindBook:
		return &catalogv1alpha1.Book{}, true
	case KindAudiobook:
		return &catalogv1alpha1.Audiobook{}, true
	case KindIssue:
		return &catalogv1alpha1.Issue{}, true
	case KindSeries:
		return &catalogv1alpha1.Series{}, true
	case KindComic:
		return &catalogv1alpha1.Comic{}, true
	}
	return nil, false
}

// Watch adds the item path's sources to b: the item arm of the MediaFile
// source, then each item kind's Watches, in kind order, then the stages'
// sources (S24, S30, the delivery wakes and each Watcher stage's own),
// deduplicated by name against seen.
func (ir *ItemReconciler) Watch(mgr ctrl.Manager, b *builder.TypedBuilder[Key], seen map[string]bool) (*builder.TypedBuilder[Key], error) {
	if len(ir.Items) == 0 {
		return b, nil
	}
	b = b.Watches(&catalogv1alpha1.MediaFile{}, ir.fileWakes())
	for _, kind := range slices.Sorted(maps.Keys(ir.Items)) {
		for _, w := range ir.Items[kind].Watches() {
			b = b.Watches(w.Object, itemHandler(kind, w.Map), builder.WithPredicates(w.Predicates...))
		}
	}
	srcs, err := ir.sources(mgr)
	if err != nil {
		return nil, err
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
	return b, nil
}

// itemHandler adapts an item package's map function to kind's keys.
func itemHandler(kind KeyKind, m handler.MapFunc) handler.TypedEventHandler[client.Object, Key] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []Key {
		reqs := m(ctx, o)
		keys := make([]Key, 0, len(reqs))
		for _, r := range reqs {
			keys = append(keys, Key{Kind: kind, Namespace: r.Namespace, Name: r.Name})
		}
		return keys
	})
}

// fileWakes is the item arm of S1 and S1' (§3.3, §2.14): a file appearing or
// going, a spec change, or a FileSignature change enqueues the keys of every
// item the file covers that has a path here. It never enqueues the file key.
// TypedFuncs enqueues the initial list's creates at handler.LowPriority.
func (ir *ItemReconciler) fileWakes() handler.TypedEventHandler[client.Object, Key] {
	add := func(q workqueue.TypedRateLimitingInterface[Key], keys []Key) {
		for _, k := range keys {
			if _, ok := ir.Items[k.Kind]; ok {
				q.Add(k)
			}
		}
	}
	return handler.TypedFuncs[client.Object, Key]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			add(q, ItemKeys(asMediaFile(e.Object)))
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			add(q, itemWakeKeys(asMediaFile(e.ObjectOld), asMediaFile(e.ObjectNew)))
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			add(q, ItemKeys(asMediaFile(e.Object)))
		},
	}
}

// itemWakeKeys is the item arm for an update: a spec change wakes the items
// of the old and the new object; a status-only change wakes the file's items
// only when an input to their rollups moved. Labels, annotations and every
// other status field wake no item.
func itemWakeKeys(old, cur *catalogv1alpha1.MediaFile) []Key {
	switch {
	case old == nil || cur == nil:
		return ItemKeys(cur)
	case old.Generation != cur.Generation:
		keys := ItemKeys(old)
		for _, k := range ItemKeys(cur) {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
		return keys
	case SignatureOf(old) != SignatureOf(cur):
		return ItemKeys(cur)
	default:
		return nil
	}
}

// FileSignature is what of a MediaFile's status the item rollups read
// (§3.3 S1'): the Transcoded verdict, the probed audio languages, whether it
// is probed at all, its Ready status, and the transcode and graft phases with
// the graft's tag. A status write that moves none of them wakes no item.
type FileSignature struct {
	Transcoded     bool
	AudioLanguages string
	Probed         bool
	Ready          metav1.ConditionStatus
	TranscodePhase catalogv1alpha1.TranscodePhase
	GraftPhase     catalogv1alpha1.GraftPhase
	GraftTag       string
}

// SignatureOf is mf's FileSignature.
func SignatureOf(mf *catalogv1alpha1.MediaFile) FileSignature {
	s := FileSignature{
		Transcoded:     rollup.Transcoded(mf),
		AudioLanguages: rollup.AudioLanguagesObject(mf),
		Probed:         mf.Status.ProbeHash != "",
	}
	if c := meta.FindStatusCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady); c != nil {
		s.Ready = c.Status
	}
	if t := mf.Status.Transcode; t != nil {
		s.TranscodePhase = t.Phase
	}
	if g := mf.Status.Graft; g != nil {
		s.GraftPhase, s.GraftTag = g.Phase, g.Tag
	}
	return s
}

func asMediaFile(o client.Object) *catalogv1alpha1.MediaFile {
	mf, _ := o.(*catalogv1alpha1.MediaFile)
	return mf
}
