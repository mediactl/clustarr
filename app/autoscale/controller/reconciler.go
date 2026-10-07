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

package controller

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Reconciler owns one HPA per Deployment labelled
// autoscale.clustarr.io/domain in Namespace, under clustarr-autoscale. It
// is leader-only, as every controller is.
type Reconciler struct {
	Client   client.Client
	Recorder k8sevents.EventRecorder
	// Namespace is the manager's own: the External Metrics API serves only
	// it, so an HPA anywhere else could never read its metric.
	Namespace string
	// Topology supplies each consumer's spec; zero means events.Default().
	Topology events.Topology
}

// SetupWithManager registers the controller "autoscale".
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	inNamespace := func(o client.Object) bool { return o.GetNamespace() == r.Namespace }
	labelled := func(o client.Object) bool { _, ok := o.GetLabels()[agentdomain.LabelDomain]; return ok }
	deployments := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return inNamespace(e.Object) && labelled(e.Object) },
		// The old object counts too: a Deployment that loses its label
		// must reach Reconcile to lose its HPA.
		UpdateFunc: func(e event.UpdateEvent) bool {
			return inNamespace(e.ObjectNew) && (labelled(e.ObjectOld) || labelled(e.ObjectNew))
		},
		// The HPA's ownerReference lets the garbage collector remove it.
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return inNamespace(e.Object) && labelled(e.Object) },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("autoscale").
		For(&appsv1.Deployment{}, builder.WithPredicates(deployments)).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}, builder.WithPredicates(hpaChanged())).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.labelledDeployments),
			builder.WithPredicates(nodeSchedulingChanged())).
		Complete(r)
}

// Reconcile applies the HPA RenderHPA declares, or deletes the one this
// Deployment owns when it is unlabelled, paused or names no autoscaled
// domain.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "autoscale.Reconcile")
	defer span.End()

	var dep appsv1.Deployment
	if err := r.Client.Get(ctx, req.NamespacedName, &dep); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	name, labelled := dep.Labels[agentdomain.LabelDomain]
	if !labelled || dep.Annotations[agentdomain.AnnotationPaused] == "true" || !dep.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.deleteOwnedHPA(ctx, &dep)
	}
	domain, ok := agentdomain.Lookup(name)
	if !ok || !domain.Autoscaled {
		r.warn(&dep, ReasonUnknownDomain, "%s=%q names no autoscaled domain; no HPA is kept", agentdomain.LabelDomain, name)
		return ctrl.Result{}, r.deleteOwnedHPA(ctx, &dep)
	}

	var existing autoscalingv2.HorizontalPodAutoscaler
	found := true
	if err := r.Client.Get(ctx, req.NamespacedName, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		found = false
	}
	if found && !controlledBy(&existing, &dep) {
		r.warn(&dep, ReasonAutoscaleConflict, "HorizontalPodAutoscaler %s exists and is not this Deployment's; the manager leaves it alone", existing.Name)
		return ctrl.Result{}, nil
	}

	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("autoscale: list Nodes: %w", err)
	}
	matching, err := MatchingNodes(nodes.Items, &dep.Spec.Template.Spec)
	if err != nil {
		r.warn(&dep, ReasonNoMatchingNodes, "%v", err)
		matching = 0
	}
	consumers, err := r.consumers(domain)
	if err != nil {
		return ctrl.Result{}, err
	}
	in := RenderInput{Deployment: &dep, Domain: domain, Consumers: consumers, MatchingNodes: matching}
	out := RenderHPA(in)
	for _, w := range out.Warnings {
		r.warn(&dep, w.Reason, "%s", w.Message)
	}

	if dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0 && (!found || stuckAtZero(&existing)) {
		if err := k8s.ScaleTo(ctx, r.Client, k8s.ManagerAutoscale, &dep, 1); err != nil {
			return ctrl.Result{}, err
		}
	}

	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerAutoscale, out.HPA); err != nil {
		if !minReplicasRejected(err) {
			return ctrl.Result{}, err
		}
		in.ForceMinOne = true
		out = RenderHPA(in)
		r.warn(&dep, ReasonScaleToZeroUnavailable, "the apiserver refused minReplicas 0 (HPAScaleToZero off, or a cluster before 1.37); the HPA keeps at least 1 replica")
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerAutoscale, out.HPA); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) consumers(d agentdomain.Domain) ([]events.ConsumerSpec, error) {
	top := r.Topology
	if len(top.Consumers) == 0 {
		top = events.Default()
	}
	out := make([]events.ConsumerSpec, 0, len(d.Consumers))
	for _, name := range d.Consumers {
		c, ok := top.Consumer(name)
		if !ok {
			return nil, fmt.Errorf("autoscale: domain %s names consumer %s, which the topology does not declare", d.Name, name)
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *Reconciler) deleteOwnedHPA(ctx context.Context, dep *appsv1.Deployment) error {
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(dep), &hpa); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !controlledBy(&hpa, dep) {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &hpa))
}

func (r *Reconciler) warn(dep *appsv1.Deployment, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(dep, nil, corev1.EventTypeWarning, reason, "Autoscale", format, args...)
	}
}

// labelledDeployments maps a Node event to every labelled Deployment in
// the namespace: a node change can move every domain's maxReplicas.
func (r *Reconciler) labelledDeployments(ctx context.Context, _ client.Object) []reconcile.Request {
	var list appsv1.DeploymentList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace), client.HasLabels{agentdomain.LabelDomain}); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

// DeleteManagedHPAs deletes every HPA in namespace labelled with a domain:
// what --autoscale=false does once at start (§9.4).
func DeleteManagedHPAs(ctx context.Context, r client.Reader, w client.Writer, namespace string) (int, error) {
	var list autoscalingv2.HorizontalPodAutoscalerList
	if err := r.List(ctx, &list, client.InNamespace(namespace), client.HasLabels{agentdomain.LabelDomain}); err != nil {
		return 0, fmt.Errorf("autoscale: list labelled HPAs: %w", err)
	}
	n := 0
	for i := range list.Items {
		if err := w.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			return n, fmt.Errorf("autoscale: delete HPA %s: %w", list.Items[i].Name, err)
		}
		n++
	}
	return n, nil
}

func controlledBy(h *autoscalingv2.HorizontalPodAutoscaler, dep *appsv1.Deployment) bool {
	ref := metav1.GetControllerOf(h)
	return ref != nil && ref.UID == dep.UID
}

// stuckAtZero is the HPA reporting scaling disabled at zero replicas that
// it did not scale there itself: a hand `kubectl scale --replicas=0`. An
// HPA wakes only a workload it scaled to zero (horizontal.go:814-822).
func stuckAtZero(h *autoscalingv2.HorizontalPodAutoscaler) bool {
	disabled, scaledByHPA := false, false
	for _, c := range h.Status.Conditions {
		switch c.Type {
		case autoscalingv2.ScalingActive:
			disabled = c.Status == corev1.ConditionFalse && c.Reason == "ScalingDisabled"
		case autoscalingv2.ScaledToZero:
			scaledByHPA = c.Status == corev1.ConditionTrue
		}
	}
	return disabled && !scaledByHPA
}

// minReplicasRejected is an Invalid apply blaming spec.minReplicas.
func minReplicasRejected(err error) bool {
	if !apierrors.IsInvalid(err) {
		return false
	}
	var st apierrors.APIStatus
	if !errors.As(err, &st) || st.Status().Details == nil {
		return false
	}
	for _, c := range st.Status().Details.Causes {
		if c.Field == "spec.minReplicas" {
			return true
		}
	}
	return false
}

// hpaChanged passes the HPA updates the reconciler acts on: a spec change
// (someone edited it) or a change in the conditions the wake guard reads.
func hpaChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok1 := e.ObjectOld.(*autoscalingv2.HorizontalPodAutoscaler)
		n, ok2 := e.ObjectNew.(*autoscalingv2.HorizontalPodAutoscaler)
		if !ok1 || !ok2 {
			return true
		}
		return o.Generation != n.Generation || stuckAtZero(o) != stuckAtZero(n)
	}}
}

// nodeSchedulingChanged passes the Node updates that can change
// MatchingNodes, not every status heartbeat.
func nodeSchedulingChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok1 := e.ObjectOld.(*corev1.Node)
		n, ok2 := e.ObjectNew.(*corev1.Node)
		if !ok1 || !ok2 {
			return true
		}
		return o.Spec.Unschedulable != n.Spec.Unschedulable || nodeReady(o) != nodeReady(n) ||
			!equality.Semantic.DeepEqual(o.Labels, n.Labels) ||
			!equality.Semantic.DeepEqual(o.Spec.Taints, n.Spec.Taints) ||
			!equality.Semantic.DeepEqual(o.Status.Allocatable, n.Status.Allocatable)
	}}
}
