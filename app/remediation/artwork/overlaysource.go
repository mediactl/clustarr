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

package artwork

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/overlay"
)

var _ remediation.Watcher = (*OverlayStage)(nil)

// Sources implements remediation.Watcher: S41, an OverlayProfile whose edit
// changes what the compact rendition is drawn from and nothing the regular
// one is (compactAloneChanged) wakes its namespace's Movies and Series, at
// low priority. The stage then finds each compact overlay stale against
// the index and asks for its render.
//
// Every other profile change is the OverlayProfile controller's
// (app/catalog/controller/overlayprofile): it publishes a render task for
// each item whose status.overlay does not record the plan, and that task
// settles both renditions. It cannot see a compact rendition, which has no
// place in status, so without this source a spec.compactGeometry edit drew
// nothing until an item was woken for some other reason.
func (s *OverlayStage) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	c := mgr.GetCache()
	return []remediation.Source{{
		Name: "S41/OverlayProfile", Object: &catalogv1alpha1.OverlayProfile{},
		Handler: handler.TypedFuncs[client.Object, remediation.Key]{
			UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
				keys := overlaidItems(ctx, c, e.ObjectNew.GetNamespace())
				if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
					pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, keys...)
					return
				}
				for _, k := range keys {
					q.Add(k)
				}
			},
		},
		Predicates: []predicate.Predicate{predicate.Funcs{
			CreateFunc:  func(event.CreateEvent) bool { return false },
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
			UpdateFunc:  func(e event.UpdateEvent) bool { return compactAloneChanged(e.ObjectOld, e.ObjectNew) },
		}},
	}}, nil
}

// compactAloneChanged reports an OverlayProfile update that moves the
// compact rendition's template and not the regular poster's profile hash.
func compactAloneChanged(before, after client.Object) bool {
	o, ok := before.(*catalogv1alpha1.OverlayProfile)
	n, ok2 := after.(*catalogv1alpha1.OverlayProfile)
	if !ok || !ok2 {
		return false
	}
	return overlayplan.ProfileHash(o.Spec) == overlayplan.ProfileHash(n.Spec) &&
		overlay.TemplateHash(overlay.CompactTemplateSpec(o.Spec)) != overlay.TemplateHash(overlay.CompactTemplateSpec(n.Spec))
}

// overlaidItems are the keys of a namespace's Movies and Series: the two
// kinds with an overlay.
func overlaidItems(ctx context.Context, r client.Reader, ns string) []remediation.Key {
	var keys []remediation.Key
	var movies catalogv1alpha1.MovieList
	if err := r.List(ctx, &movies, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("artwork: list Movies for an overlay profile's compact geometry", "namespace", ns, "err", err)
	}
	for i := range movies.Items {
		keys = append(keys, remediation.Key{Kind: remediation.KindMovie, Namespace: ns, Name: movies.Items[i].Name})
	}
	var series catalogv1alpha1.SeriesList
	if err := r.List(ctx, &series, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("artwork: list Series for an overlay profile's compact geometry", "namespace", ns, "err", err)
	}
	for i := range series.Items {
		keys = append(keys, remediation.Key{Kind: remediation.KindSeries, Namespace: ns, Name: series.Items[i].Name})
	}
	return keys
}
