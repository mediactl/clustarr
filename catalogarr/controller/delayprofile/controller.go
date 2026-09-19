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

package delayprofile

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Reconciler reports whether a DelayProfile is usable. DelayProfile carries
// no CEL beyond field-level bounds the apiserver already enforces, so this is
// thin: it exists for the ObservedGeneration/Ready contract every kind
// shares, and as the watch point a later change (e.g. resolving
// status.pendingCount) can hang off.
//
// status.pendingCount is explicitly NOT written here: computing it needs the
// clustarr-pending KV bucket's grab leases, which belong to the grab worker
// (Task C9), not this controller. It is left at its zero value; whether that
// worker becomes a second field manager on DelayProfileStatus (a la the
// MediaFile split) or asks this controller to recompute it via a watch is
// that task's decision to make and document.
type Reconciler struct {
	Client   client.Client
	Recorder events.EventRecorder
}

// NewReconciler builds a Reconciler.
func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "delayprofile.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("delayprofile", req.NamespacedName)

	var dp catalogv1alpha1.DelayProfile
	if err := r.Client.Get(ctx, req.NamespacedName, &dp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), dp.Status.Conditions...)
	k8s.MarkReady(&dp, &conditions, true, k8s.ReasonReconciled, "field-level validation only; CRD schema covers everything else")

	ac := catalogac.DelayProfile(dp.Name, dp.Namespace).WithStatus(
		catalogac.DelayProfileStatus().
			WithObservedGeneration(dp.Generation).
			WithConditions(k8s.ConditionACs(conditions)...),
	)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
		log.Error("patch status", "error", err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=delayprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=delayprofiles/status,verbs=get;update;patch
// The Recorder here is a k8s.io/client-go/tools/events.EventRecorder, which
// writes events.k8s.io/v1 -- unlike the record.EventRecorder the Movie,
// Series, Episode, MediaFile and Search reconcilers take, which writes
// core/v1. This marker was simply missing, so the one warning this controller
// can emit would have been denied.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SetupWithManager registers the DelayProfile controller.
//
// The marker block above is deliberately separated from this declaration by a
// blank line. controller-gen only collects +kubebuilder:rbac markers from
// PACKAGE-level comments, and a comment group touching a declaration is that
// declaration's doc comment instead -- so until Task C12a every marker in this
// file was silently discarded and none of these permissions reached
// config/rbac/role.yaml. Nothing reports it: controller-gen exits 0 and
// envtest does not enforce RBAC. cmd/clustarr's TestRBACMarkersArePackageLevel
// is the guard.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("delayprofile").
		For(&catalogv1alpha1.DelayProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
