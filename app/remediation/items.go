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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ItemReconciler is the loop's item path (loop spec §3.12): one rollup.Item
// per item kind, reconciled by its own ReconcileItem, woken by its own
// non-file watches adapted to Key and by the item arm of the MediaFile
// source (S1, S1'), which replaced every item kind's MediaFile watch.
type ItemReconciler struct {
	Items map[KeyKind]rollup.Item
}

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
	res, err := it.ReconcileItem(ctx, types.NamespacedName{Namespace: k.Namespace, Name: k.Name})
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

// Watch adds the item path's sources to b: the item arm of the MediaFile
// source, then each item kind's Watches, in kind order.
func (ir *ItemReconciler) Watch(b *builder.TypedBuilder[Key]) *builder.TypedBuilder[Key] {
	if len(ir.Items) == 0 {
		return b
	}
	b = b.Watches(&catalogv1alpha1.MediaFile{}, ir.fileWakes())
	for _, kind := range slices.Sorted(maps.Keys(ir.Items)) {
		for _, w := range ir.Items[kind].Watches() {
			b = b.Watches(w.Object, itemHandler(kind, w.Map), builder.WithPredicates(w.Predicates...))
		}
	}
	return b
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
