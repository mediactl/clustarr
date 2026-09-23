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

package indexerproxy

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// recheckInterval is how often a proxy is re-probed between spec changes. Not
// given by the spec; invented here. One TCP dial or one small GET every five
// minutes is cheap enough to run against every proxy an install has, and
// frequent enough that a FlareSolverr which died is not still reported Ready
// an hour later. It is also the retry for the missing-Secret path, which no
// watch would otherwise wake: this controller watches IndexerProxy only.
const recheckInterval = 5 * time.Minute

// ReasonUnreachable is the Ready reason for a proxy that did not answer. See
// pkg/k8s.Reason* for the shared tokens this reuses.
const ReasonUnreachable = "ProxyUnreachable"

// Reconciler probes an IndexerProxy's reachability and reports Ready, when it
// last looked, and what version answered.
type Reconciler struct {
	Client client.Client

	// Recorder is optional; a nil Recorder disables events rather than
	// panicking.
	Recorder events.EventRecorder

	// Probe is the reachability check. NewReconciler installs the real one;
	// a test injects its own.
	Probe Prober
}

// NewReconciler builds a Reconciler probing with httpClient (nil means
// http.DefaultClient). recorder comes from mgr.GetEventRecorder and writes
// events.k8s.io/v1 Events.
func NewReconciler(c client.Client, recorder events.EventRecorder, httpClient *http.Client) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder, Probe: NewProber(httpClient, nil)}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "indexerproxy.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("indexerproxy", req.NamespacedName)

	var pxy indexv1alpha1.IndexerProxy
	if err := r.Client.Get(ctx, req.NamespacedName, &pxy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), pxy.Status.Conditions...)

	// Seeded from the live status so that both early returns below re-send
	// every field they cannot recompute instead of releasing it.
	owned := probeStateFrom(pxy.Status)

	if err := validateSpec(pxy.Spec); err != nil {
		tracing.RecordError(span, err)
		log.Warn("spec is not addressable", "error", err)
		k8s.MarkReady(&pxy, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		if r.Recorder != nil {
			r.Recorder.Eventf(&pxy, nil, corev1.EventTypeWarning, k8s.ReasonInvalidSpec, "Reconcile", "%s", err.Error())
		}
		// The complete owned set, not a conditions-only apply: the version and
		// the timestamp below are what the proxy last reported, and an apply
		// that omitted them would release them.
		if perr := r.patch(ctx, &pxy, owned, conditions); perr != nil {
			return ctrl.Result{}, perr
		}
		// Terminal: only an edit can make an unaddressable proxy addressable,
		// and the GenerationChanged predicate already delivers that.
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	if ref := pxy.Spec.SecretRef; ref != nil {
		if err := r.secretExists(ctx, pxy.Namespace, ref.Name); err != nil {
			tracing.RecordError(span, err)
			log.Info("proxy credentials are not available yet", "secret", ref.Name, "error", err)
			k8s.MarkReady(&pxy, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
			// Same obligation on this path, which is the transient one: a
			// Secret that has not been created yet is a blip, and a blip must
			// not gut a healthy object. Note lastCheckedAt is NOT stamped
			// here -- nothing was probed.
			if perr := r.patch(ctx, &pxy, owned, conditions); perr != nil {
				return ctrl.Result{}, perr
			}
			// Requeue rather than wait for a watch: this controller watches
			// IndexerProxy only, so the Secret appearing wakes nothing.
			return ctrl.Result{RequeueAfter: recheckInterval}, nil
		}
	}

	version, probeErr := r.Probe(ctx, pxy.Spec)
	now := metav1.Now()
	owned.LastCheckedAt = &now
	if version != "" {
		// Only overwrite on a positive answer: a FlareSolverr that stopped
		// responding keeps its last known version beside Ready=False, and a
		// SOCKS proxy (which reports none) never clobbers anything.
		owned.Version = version
	}

	if probeErr != nil {
		tracing.RecordError(span, probeErr)
		log.Info("proxy is unreachable", "error", probeErr)
		k8s.MarkReady(&pxy, &conditions, false, ReasonUnreachable, "%s", probeErr.Error())
	} else {
		k8s.MarkReady(&pxy, &conditions, true, k8s.ReasonReconciled, "reachable")
	}

	if err := r.patch(ctx, &pxy, owned, conditions); err != nil {
		return ctrl.Result{}, err
	}
	// A probe failure is reported, not returned: returning it would make
	// controller-runtime retry on its own backoff on top of this interval,
	// and an unreachable proxy is a state to display, not an error to raise.
	return ctrl.Result{RequeueAfter: recheckInterval}, nil
}

// probeState is the part of the owned set that survives across reconciles.
type probeState struct {
	LastCheckedAt *metav1.Time
	Version       string
}

// probeStateFrom seeds the owned set from the live status.
func probeStateFrom(st indexv1alpha1.IndexerProxyStatus) probeState {
	return probeState{LastCheckedAt: st.LastCheckedAt, Version: st.Version}
}

// validateSpec rejects a proxy that cannot be addressed at all.
//
// spec.port is +optional in the CRD with no default, but a proxy is addressed
// as host:port and there is no defensible default across flaresolverr (8191),
// http (3128, 8080, 8888, ...) and socks (1080). Guessing one would probe
// something the operator never configured and report it Ready. So an absent
// port is reported as an invalid spec instead -- visible, and fixable by an
// edit -- rather than guessed at.
func validateSpec(spec indexv1alpha1.IndexerProxySpec) error {
	if spec.Host == "" {
		return fmt.Errorf("spec.host is empty")
	}
	if spec.Port <= 0 || spec.Port > 65535 {
		return fmt.Errorf("spec.port is %d: a proxy is addressed as host:port and the CRD sets no default", spec.Port)
	}
	switch spec.Type {
	case indexv1alpha1.IndexerProxyTypeFlareSolverr,
		indexv1alpha1.IndexerProxyTypeHTTP,
		indexv1alpha1.IndexerProxyTypeSocks4,
		indexv1alpha1.IndexerProxyTypeSocks5:
		return nil
	default:
		return fmt.Errorf("spec.type %q is not one of flaresolverr, http, socks4, socks5", spec.Type)
	}
}

// secretExists checks that spec.secretRef resolves. The contents are NOT read:
// the credentials are M6's, and checking now is only so a typo in the name is
// visible before then.
func (r *Reconciler) secretExists(ctx context.Context, namespace, name string) error {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		return fmt.Errorf("spec.secretRef %q: %w", name, err)
	}
	return nil
}

// patch is the only status write in this package, so that "declare the
// complete owned set" is enforced in one place rather than remembered at four
// call sites.
//
// lastCheckedAt is the single shape exception: it is a *metav1.Time whose zero
// value marshals to null, which the CRD's format: date-time rejects, so it is
// omitted while nil -- which is only ever true before the first probe.
func (r *Reconciler) patch(
	ctx context.Context,
	pxy *indexv1alpha1.IndexerProxy,
	s probeState,
	conditions []metav1.Condition,
) error {
	status := indexac.IndexerProxyStatus().
		WithObservedGeneration(pxy.Generation).
		WithConditions(k8s.ConditionACs(conditions)...).
		WithVersion(s.Version)
	if s.LastCheckedAt != nil {
		status = status.WithLastCheckedAt(*s.LastCheckedAt)
	}

	ac := indexac.IndexerProxy(pxy.Name, pxy.Namespace).WithStatus(status)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerIndexarr, ac); err != nil {
		logging.FromContext(ctx).Error("patch status", "error", err)
		return err
	}
	return nil
}

// +kubebuilder:rbac markers for this package live in doc.go, at package level.
// controller-gen collects them only from a comment group that is not attached
// to a declaration, and a block placed here, above SetupWithManager, becomes
// that function's doc comment and is silently discarded -- which is exactly
// how four Phase C controllers shipped with no permissions at all.

// SetupWithManager registers the IndexerProxy controller. Task D1-8 calls
// NewReconciler(...).SetupWithManager(mgr); see doc.go for the exact call.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("indexerproxy").
		For(&indexv1alpha1.IndexerProxy{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
