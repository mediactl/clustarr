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

package qualityprofile

import (
	"context"
	"strings"
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
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const ReasonResolutionFailed = "ResolutionFailed"

// Reconciler resolves a QualityProfile against the loaded custom-format
// Catalogue and reports whether it is usable.
type Reconciler struct {
	Client    client.Client
	Catalogue *catalogue.Catalogue
	Recorder  events.EventRecorder
}

// NewReconciler builds a Reconciler that resolves every QualityProfile
// against cat.
func NewReconciler(c client.Client, cat *catalogue.Catalogue, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Catalogue: cat, Recorder: recorder}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "qualityprofile.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("qualityprofile", req.Name)

	var qp catalogv1alpha1.QualityProfile
	if err := r.Client.Get(ctx, req.NamespacedName, &qp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	profile, errs := quality.FromCRD(&qp, r.Catalogue)
	conditions := append([]metav1.Condition(nil), qp.Status.Conditions...)

	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		message := strings.Join(msgs, "; ")
		k8s.MarkTrue(&qp, &conditions, catalogv1alpha1.QualityProfileConditionInvalid, ReasonResolutionFailed, "%s", message)
		k8s.MarkReady(&qp, &conditions, false, ReasonResolutionFailed, "%s", message)
		if r.Recorder != nil {
			r.Recorder.Eventf(&qp, nil, "Warning", ReasonResolutionFailed, "Reconcile", message)
		}
	} else {
		k8s.MarkFalse(&qp, &conditions, catalogv1alpha1.QualityProfileConditionInvalid, k8s.ReasonReconciled, "resolved cleanly")
		k8s.MarkReady(&qp, &conditions, true, k8s.ReasonReconciled, "resolved against catalogue %s", r.Catalogue.Version)
	}

	var order []string
	for _, tier := range profile.Tiers {
		for _, def := range tier {
			order = append(order, def.Name)
		}
	}

	ac := catalogac.QualityProfile(qp.Name).WithStatus(
		catalogac.QualityProfileStatus().
			WithObservedGeneration(qp.Generation).
			WithCatalogueVersion(r.Catalogue.Version).
			WithResolvedFormats(int32(len(profile.Scores))).
			WithQualityOrder(order...).
			WithHash(profile.Hash).
			WithConditions(k8s.ConditionACs(conditions)...),
	)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
		log.Error("patch status", "error", err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles/status,verbs=get;update;patch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SetupWithManager registers the QualityProfile controller.
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
		Named("qualityprofile").
		For(&catalogv1alpha1.QualityProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
