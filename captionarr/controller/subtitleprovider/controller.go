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

// Package subtitleprovider reconciles SubtitleProvider: ruling R2 makes it
// the ONLY writer of SubtitleProvider.status. Every fetch worker (task F-5)
// records throttles, quota and cached auth into the shared
// clustarr-provider-throttle KV bucket (captionarr/throttle) instead of
// touching this object directly -- captionarr/status.PatchProvider refuses
// every field manager except k8s.ManagerCaptionarr, which is R2 enforced in
// code, not just documented.
//
// This package validates and reports; it never builds a real
// pkg/subtitles.Provider client or calls an upstream API. Task F-5 owns the
// builder that turns a SubtitleProvider + Secret into one.
package subtitleprovider

import (
	"context"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"
	corev1 "k8s.io/api/core/v1"
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

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	captionarrstatus "github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// kvPollInterval is the steady-state requeue for an enabled, implemented
// provider, mirroring indexarr/controller/indexer's identical reprobeInterval
// and its own doc comment's reasoning verbatim: status.throttledUntil,
// .quota, .errorsLast120s and .lastSuccessAt are all derived from the shared
// KV bucket every fetch worker writes, under a DIFFERENT field manager this
// controller never watches (ruling R2 -- a worker never touches this object,
// so there is no watch to build). Nothing else wakes this reconcile when
// that KV state changes, so this tick is what keeps it from going stale.
const kvPollInterval = 15 * time.Minute

// This controller's own RBAC. SubtitleProvider is namespaced. secrets is
// read-only and get;list;watch, matching
// catalogarr/controller/metadataprovider/controller.go's identical marker
// for the identical shape (reading a provider's credential Secret by name).
//
// The blank line below is load-bearing -- see
// cmd/clustarr.TestRBACMarkersArePackageLevel: controller-gen only collects
// +kubebuilder:rbac from a comment group that is NOT a declaration's doc
// comment, and attaching this block to SetupWithManager would make every
// rule in it silently absent from config/rbac/role.yaml.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler owns SubtitleProvider.status (under k8s.ManagerCaptionarr, via
// captionarr/status.PatchProvider) and writes nothing else. See ruling R2 and
// the package doc comment.
type Reconciler struct {
	client.Client

	// KV is the clustarr-provider-throttle bucket (events.BucketProviderThrottle)
	// every fetch worker writes and this reconciler alone reads, via
	// captionarr/throttle.Get. A nil KV is a caller error, not a valid "no
	// bus configured" state -- captionarr's own Options.Validate requires
	// --nats-url precisely because this bucket is load-bearing.
	KV events.KV

	Recorder k8sevents.EventRecorder
	Clock    clockwork.Clock
}

// NewReconciler builds a Reconciler with the real clock.
func NewReconciler(c client.Client, kv events.KV, recorder k8sevents.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, KV: kv, Recorder: recorder, Clock: clockwork.NewRealClock()}
}

func (r *Reconciler) now() time.Time {
	if r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

// Reconcile validates sp's credentials (for an implemented provider type;
// see ruling R5), projects the shared KV throttle state into
// status.throttledUntil/throttleReason/quota/tokenExpiresAt/lastSuccessAt
// /errorsLast120s, and derives Ready/Authenticated/Throttled. It never
// writes state.JWT anywhere in status -- SubtitleProviderStatus has no field
// for it, and [throttle.State]'s own doc comment says why: "credentials do
// not belong on a CRD".
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "subtitleprovider.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("subtitleprovider", req.NamespacedName)

	var sp subtitlev1alpha1.SubtitleProvider
	if err := r.Get(ctx, req.NamespacedName, &sp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&sp) {
		return ctrl.Result{}, nil
	}

	impl := implemented(sp.Spec.Type)

	var (
		auth  authResult
		state throttle.State
	)
	if impl {
		secret, err := r.getSecret(ctx, sp.Namespace, sp.Spec.SecretRef)
		var secretErr error
		switch {
		case err == nil:
			// secret is either populated (SecretRef set and found) or nil
			// (SecretRef unset); checkAuthentication tells those apart.
		case apierrors.IsNotFound(err):
			secretErr = err
		default:
			return ctrl.Result{}, fmt.Errorf("subtitleprovider: get secret %s/%s: %w", sp.Namespace, sp.Spec.SecretRef.Name, err)
		}
		auth = checkAuthentication(sp.Spec.Type, sp.Spec.SecretRef, secret, secretErr)

		if r.KV == nil {
			return ctrl.Result{}, fmt.Errorf("subtitleprovider: nil KV; captionarr must be started with --nats-url")
		}
		state, err = throttle.Get(ctx, r.KV, string(sp.UID))
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("subtitleprovider: read throttle state for %s/%s: %w", sp.Namespace, sp.Name, err)
		}
	}

	now := r.now()
	throttled := impl && state.Throttled(now)

	if impl && !auth.authenticated && r.Recorder != nil {
		r.Recorder.Eventf(&sp, nil, "Warning", auth.reason, "Reconcile", auth.message)
	}

	// Re-Get immediately before the status apply: the Secret Get and the KV
	// Get above are exactly the read-then-work-then-apply shape CLAUDE.md's
	// "lost update" hazard describes. sp is seeded fresh so the apply
	// carries forward whatever concurrent state landed since the Get at the
	// top of this func.
	var fresh subtitlev1alpha1.SubtitleProvider
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), fresh.Status.Conditions...)
	switch {
	case !impl:
		message := fmt.Sprintf("captionarr has no client for provider type %q yet", sp.Spec.Type)
		k8s.MarkUnknown(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated, ReasonNotImplemented, "%s", message)
		k8s.MarkUnknown(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled, ReasonNotImplemented, "%s", message)
		k8s.MarkReady(&fresh, &conditions, false, ReasonNotImplemented, "%s", message)
	default:
		if auth.authenticated {
			k8s.MarkTrue(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated, auth.reason, "%s", auth.message)
		} else {
			k8s.MarkFalse(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated, auth.reason, "%s", auth.message)
		}
		if throttled {
			reason := state.ThrottleReason
			if reason == "" {
				reason = k8s.ReasonThrottled
			}
			k8s.MarkTrue(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled, reason,
				"throttled until %s", state.ThrottledUntil.Format(time.RFC3339))
		} else {
			k8s.MarkFalse(&fresh, &conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled, ReasonNotThrottled, "not throttled")
		}
		switch {
		case !sp.Spec.Enabled:
			k8s.MarkReady(&fresh, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
		case !auth.authenticated:
			k8s.MarkReady(&fresh, &conditions, false, k8s.ReasonDependencyNotReady, "not authenticated: %s", auth.message)
		case throttled:
			k8s.MarkReady(&fresh, &conditions, false, k8s.ReasonThrottled, "throttled until %s", state.ThrottledUntil.Format(time.RFC3339))
		default:
			k8s.MarkReady(&fresh, &conditions, true, k8s.ReasonReconciled, "enabled, authenticated and not throttled")
		}
	}

	err := captionarrstatus.PatchProvider(ctx, r.Client, k8s.ManagerCaptionarr, &fresh,
		func(ac *subtitleac.SubtitleProviderStatusApplyConfiguration) {
			applyThrottleState(ac, fresh.Generation, impl, sp.Spec.Type, state)
			ac.WithConditions(k8s.ConditionACs(conditions)...)
		})
	if err != nil {
		return ctrl.Result{}, err
	}

	result := ctrl.Result{}
	if impl && sp.Spec.Enabled {
		result.RequeueAfter = nextRequeue(now, state)
	}
	log.Debug("reconciled", "ready", k8s.IsConditionTrue(conditions, k8s.ConditionReady), "requeueAfter", result.RequeueAfter)
	return result, nil
}

// getSecret fetches the Secret ref names in namespace ns, returning
// (secret, nil) on success or (nil, err) otherwise -- err may be a NotFound
// (apierrors.IsNotFound), which the caller treats as a reportable condition
// rather than a reconcile failure, or any other error, which it does not.
// ref == nil returns (nil, nil): checkAuthentication interprets a nil secret
// with a nil error as "no Secret named at all" itself.
func (r *Reconciler) getSecret(ctx context.Context, ns string, ref *corev1.LocalObjectReference) (*corev1.Secret, error) {
	if ref == nil {
		return nil, nil
	}
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// applyThrottleState renders every leaf ProviderFields' caller must declare,
// from state -- the fresh read from KV, never carried forward from the
// object's stale status -- clearing a leaf explicitly (assigning the
// generated field nil) when state no longer carries it, per
// captionarr/status.ProviderFields' own doc comment on ThrottledUntil,
// TokenExpiresAt and LastSuccessAt: "a caller assigns the returned
// configuration's field directly ... the same pattern grabarr/status
// documents for BlocklistedUntil". state.JWT is never read here.
func applyThrottleState(
	ac *subtitleac.SubtitleProviderStatusApplyConfiguration,
	generation int64,
	impl bool,
	providerType subtitlev1alpha1.SubtitleProviderType,
	state throttle.State,
) {
	ac.WithObservedGeneration(generation).
		WithThrottleReason(state.ThrottleReason).
		WithErrorsLast120s(state.ErrorsLast120s).
		WithHIVerifiable(impl && hiVerifiable(providerType))

	if state.ThrottledUntil != nil {
		ac.WithThrottledUntil(metav1.NewTime(*state.ThrottledUntil))
	} else {
		ac.ThrottledUntil = nil
	}
	if state.TokenExpiresAt != nil {
		ac.WithTokenExpiresAt(metav1.NewTime(*state.TokenExpiresAt))
	} else {
		ac.TokenExpiresAt = nil
	}
	if state.LastSuccessAt != nil {
		ac.WithLastSuccessAt(metav1.NewTime(*state.LastSuccessAt))
	} else {
		ac.LastSuccessAt = nil
	}
	if state.Quota != nil {
		resetAt := metav1.Time{}
		if state.Quota.ResetAt != nil {
			resetAt = metav1.NewTime(*state.Quota.ResetAt)
		}
		ac.WithQuota(subtitleac.ProviderQuota().WithRemaining(state.Quota.Remaining).WithResetAt(resetAt))
	} else {
		ac.Quota = nil
	}
}

// nextRequeue is the sooner of [kvPollInterval]'s steady-state tick and the
// remaining time until state's throttle expires, per the F-3 brief: "requeue
// so a throttle that expires is reflected without an external event, since
// nothing about a KV write wakes a controller." A throttle already expired
// (or never set) contributes nothing shorter than the steady tick.
func nextRequeue(now time.Time, state throttle.State) time.Duration {
	d := kvPollInterval
	if state.ThrottledUntil != nil {
		if until := state.ThrottledUntil.Sub(now); until > 0 && until < d {
			d = until
		}
	}
	return d
}

// SetupWithManager registers the SubtitleProvider controller. captionarr's
// run.go setupControllers wires this in as task F-6 (out of this task's
// scope; this package is not imported from run.go yet).
//
// There is deliberately no Watches on corev1.Secret, matching
// catalogarr/controller/metadataprovider's identical choice for the
// identical shape: a Secret edit is picked up within [kvPollInterval] by the
// steady-state requeue every enabled, implemented provider already
// schedules, rather than by a dedicated watch.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("subtitleprovider").
		For(&subtitlev1alpha1.SubtitleProvider{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{
			RecoverPanic:          ptr.To(true),
			ReconciliationTimeout: 5 * time.Minute,
		}).
		Complete(r)
}
