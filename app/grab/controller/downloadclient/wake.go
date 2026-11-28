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

package downloadclient

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// A fleet's budgets and engine count follow what changes, not a timer
// (architect's review, 2026-11-28, against ADR-0025's direction): the
// engines' records wake the controller already; the downloads stage wakes
// it through a Wake when the grabs waiting for or holding a client's
// transfer slots change; and a node's bandwidth annotations wake the
// clients they budget.

// Wake is a source the downloads stage signals: a DownloadClient whose
// slot waiters or holders changed. internal/cli/manager makes one and hands
// it to both.
type Wake struct {
	mu    sync.Mutex
	queue workqueue.TypedRateLimitingInterface[reconcile.Request]
}

// NewWake builds a Wake.
func NewWake() *Wake { return &Wake{} }

var _ crsource.TypedSource[reconcile.Request] = (*Wake)(nil)

// Start implements source.TypedSource: it keeps the controller's queue.
func (w *Wake) Start(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	w.mu.Lock()
	w.queue = q
	w.mu.Unlock()
	return nil
}

// String implements fmt.Stringer for the controller's logs.
func (*Wake) String() string { return "downloadclient slot wake" }

// Signal enqueues the DownloadClient namespace/name. Before the controller
// starts it does nothing: the controller's first pass reads what changed.
func (w *Wake) Signal(namespace, name string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	q := w.queue
	w.mu.Unlock()
	if q != nil {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	}
}

// bandwidthAnnotations are the node annotations an engine's budget reads.
var bandwidthAnnotations = []string{
	downloadv1alpha1.NodeLinkSpeedAnnotation,
	downloadv1alpha1.NodeBandwidthRequestAnnotation,
	downloadv1alpha1.NodeBandwidthLimitAnnotation,
}

// nodeBandwidthChanged passes a node whose bandwidth annotations changed,
// and a node that appears or goes with any: nothing else about a node
// moves a budget, and a node's status changes every few minutes.
func nodeBandwidthChanged() predicate.Predicate {
	has := func(o client.Object) bool {
		for _, k := range bandwidthAnnotations {
			if o.GetAnnotations()[k] != "" {
				return true
			}
		}
		return false
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return has(e.Object) },
		DeleteFunc: func(e event.DeleteEvent) bool { return has(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			for _, k := range bandwidthAnnotations {
				if e.ObjectOld.GetAnnotations()[k] != e.ObjectNew.GetAnnotations()[k] {
					return true
				}
			}
			return false
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// budgetedClients maps a node to every DownloadClient with a bandwidth
// budget: a handful, and any of them may have an engine there.
func (r *Reconciler) budgetedClients(ctx context.Context, _ *corev1.Node) []reconcile.Request {
	var list downloadv1alpha1.DownloadClientList
	if err := r.Client.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Warn("downloadclient: list clients for a node's bandwidth", "err", err)
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		if u := list.Items[i].Spec.Usenet; u != nil && u.Bandwidth != nil {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return out
}
