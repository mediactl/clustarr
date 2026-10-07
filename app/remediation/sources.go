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

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// watchedAnnotations wake a file at watch priority (§3.3, S1): importarr's
// observed fingerprint, the DLQ projector's dead-lettered marker (folded into
// DeadLettered, §2.8) and an operator's replay request (the replay actuator,
// §3.9).
var watchedAnnotations = []string{mediafile.AnnotationObservedFingerprint, k8s.AnnotationDeadLettered, history.AnnotationReplay}

// intentAnnotations wake a file at PriorityUser (§2.9).
var intentAnnotations = catalogv1alpha1.IntentAnnotations()

// Source is one watch a planner or actuator adds to the loop's controller:
// Raw for a records waker (WatchesRawSource), else Object with Handler.
// Name dedups a source two planners both need ("S23/TranscodeJob").
type Source struct {
	Name       string
	Object     client.Object
	Handler    handler.TypedEventHandler[client.Object, Key]
	Predicates []predicate.Predicate
	Raw        source.TypedSource[Key]
}

// Watcher is a planner or actuator with sources beyond S1.
type Watcher interface {
	Sources(mgr ctrl.Manager) ([]Source, error)
}

// MapFiles adapts a map function from an object to the files it concerns.
func MapFiles(fn func(context.Context, client.Object) []types.NamespacedName) handler.TypedEventHandler[client.Object, Key] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []Key {
		nns := fn(ctx, o)
		out := make([]Key, 0, len(nns))
		for _, nn := range nns {
			out = append(out, Key{Kind: KindMediaFile, Namespace: nn.Namespace, Name: nn.Name})
		}
		return out
	})
}

// mediaFileEvents is S1's file arm: create and delete always; an update by
// fileUpdateWakes; the initial list at handler.LowPriority (TypedFuncs). A
// status-only update never maps to the file key (the self-trigger rule);
// S1' and the item keys of S1 are the item arm, ItemReconciler.fileWakes,
// registered on the same controller (§3.3).
func mediaFileEvents() handler.TypedEventHandler[client.Object, Key] {
	key := func(o client.Object) Key {
		return Key{Kind: KindMediaFile, Namespace: o.GetNamespace(), Name: o.GetName()}
	}
	return handler.TypedFuncs[client.Object, Key]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			q.Add(key(e.Object))
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			wake, user := fileUpdateWakes(e.ObjectOld, e.ObjectNew)
			switch {
			case !wake:
			case user:
				addWithPriority(q, key(e.ObjectNew), PriorityUser)
			default:
				q.Add(key(e.ObjectNew))
			}
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			q.Add(key(e.Object))
		},
	}
}

// fileUpdateWakes is S1's update predicate: wake on a generation change, a
// watched annotation, an intent annotation or a user's label (the last two
// at PriorityUser); never on a status-only update or the loop's own labels.
func fileUpdateWakes(old, nw client.Object) (wake, user bool) {
	if old.GetGeneration() != nw.GetGeneration() {
		wake = true
	}
	differs := func(k string) bool {
		a, aok := old.GetAnnotations()[k]
		b, bok := nw.GetAnnotations()[k]
		return a != b || aok != bok
	}
	for _, k := range watchedAnnotations {
		if differs(k) {
			wake = true
		}
	}
	for _, k := range intentAnnotations {
		if differs(k) {
			wake, user = true, true
		}
	}
	if userLabelsDiffer(old.GetLabels(), nw.GetLabels()) {
		wake, user = true, true
	}
	return wake, user
}

// userLabelsDiffer reports a change to any label but the loop's own.
func userLabelsDiffer(a, b map[string]string) bool {
	skip := map[string]bool{}
	for _, k := range mediafile.LoopLabelKeys {
		skip[k] = true
	}
	for k, v := range a {
		if bv, ok := b[k]; !skip[k] && (!ok || bv != v) {
			return true
		}
	}
	for k := range b {
		if _, ok := a[k]; !skip[k] && !ok {
			return true
		}
	}
	return false
}

func addWithPriority(q workqueue.TypedRateLimitingInterface[Key], k Key, p int) {
	if pq, ok := q.(priorityqueue.PriorityQueue[Key]); ok {
		pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(p)}, k)
		return
	}
	q.Add(k)
}
