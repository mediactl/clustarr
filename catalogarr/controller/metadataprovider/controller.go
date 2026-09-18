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

package metadataprovider

import (
	"context"
	"errors"
	"net/http"
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
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// reprobeInterval is how often a healthy provider is re-probed. Not given by
// the spec; invented -- frequent enough to notice a key rotation or an
// outage within the hour, cheap enough (one call) not to threaten any
// provider's own rate limit even at the lowest configured RPS.
const reprobeInterval = 15 * time.Minute

const ReasonProviderNotImplemented = "ProviderNotImplemented"

// Reconciler probes a MetadataProvider's reachability and credentials on a
// timer and reports Ready/Authenticated/Throttled.
type Reconciler struct {
	Client     client.Client
	Recorder   events.EventRecorder
	HTTPClient *http.Client
}

// NewReconciler builds a Reconciler that probes providers with httpClient.
func NewReconciler(c client.Client, recorder events.EventRecorder, httpClient *http.Client) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder, HTTPClient: httpClient}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "metadataprovider.Reconcile")
	defer span.End()

	var mp catalogv1alpha1.MetadataProvider
	if err := r.Client.Get(ctx, req.NamespacedName, &mp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	conditions := append([]metav1.Condition(nil), mp.Status.Conditions...)

	if mp.Spec.Enabled != nil && !*mp.Spec.Enabled {
		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
	}

	secretData, err := readSecret(ctx, r.Client, mp.Namespace, mp.Spec.SecretRef)
	if err != nil {
		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{RequeueAfter: reprobeInterval})
	}

	prober, err := NewProber(mp.Spec, secretData, r.HTTPClient)
	if err == ErrProviderNotImplemented {
		k8s.SetCondition(&mp, &conditions, k8s.NewCondition(catalogv1alpha1.MetadataProviderConditionReady, metav1.ConditionUnknown, ReasonProviderNotImplemented, "no client exists for provider type %s yet", mp.Spec.Type))
		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
	}
	if err != nil {
		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
	}

	result, probeErr := prober.Probe(ctx)
	switch {
	case probeErr == nil:
		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, k8s.ReasonReconciled, "credentials accepted")
		k8s.MarkFalse(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionThrottled, k8s.ReasonReconciled, "not throttled")
		k8s.MarkReady(&mp, &conditions, true, k8s.ReasonReconciled, "reachable")
	case isRateLimited(probeErr):
		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, k8s.ReasonReconciled, "credentials previously accepted")
		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionThrottled, k8s.ReasonThrottled, "%s", probeErr.Error())
		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonThrottled, "%s", probeErr.Error())
	case isAuthError(probeErr):
		k8s.MarkFalse(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, "CredentialsRejected", "%s", probeErr.Error())
		k8s.MarkReady(&mp, &conditions, false, "CredentialsRejected", "%s", probeErr.Error())
		if r.Recorder != nil {
			r.Recorder.Eventf(&mp, nil, "Warning", "CredentialsRejected", "Reconcile", probeErr.Error())
		}
	default:
		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonReconcileError, "%s", probeErr.Error())
	}

	return r.patch(ctx, &mp, conditions, result.QuotaRemaining, ctrl.Result{RequeueAfter: reprobeInterval})
}

func (r *Reconciler) patch(ctx context.Context, mp *catalogv1alpha1.MetadataProvider, conditions []metav1.Condition, quotaRemaining *int32, result ctrl.Result) (ctrl.Result, error) {
	statusAC := catalogac.MetadataProviderStatus().
		WithObservedGeneration(mp.Generation).
		WithConditions(k8s.ConditionACs(conditions)...)
	if quotaRemaining != nil {
		statusAC = statusAC.WithQuotaRemaining(*quotaRemaining)
	}
	ac := catalogac.MetadataProvider(mp.Name, mp.Namespace).WithStatus(statusAC)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
		logging.FromContext(ctx).Error("patch status", "error", err)
		return ctrl.Result{}, err
	}
	return result, nil
}

// isRateLimited reports whether err is or wraps *metadata.RateLimitedError.
func isRateLimited(err error) bool {
	var rateLimited *metadata.RateLimitedError
	return errors.As(err, &rateLimited)
}

// isAuthError reports whether err is or wraps metadata.ErrAuth.
func isAuthError(err error) bool {
	return errors.Is(err, metadata.ErrAuth)
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("metadataprovider").
		For(&catalogv1alpha1.MetadataProvider{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
