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

package indexer

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// definitionRetryInterval is how soon a definition-backed Indexer whose
// definition is missing is looked at again. The IndexerDefinition watch
// (SetupWithManager) already wakes the Indexer when its definition appears --
// "I created the Indexer first and the definition second" is the ordinary
// kubectl apply -f dir/ race -- so this is the backstop for a missed event.
const definitionRetryInterval = time.Minute

// reconcileDefinition is Reconcile for spec.definition and spec.definitionRef.
//
// A Cardigann indexer has no t=caps endpoint: its capabilities, protocol and
// privacy ARE the definition, so they are resolved from it on every pass
// (pure, no I/O) rather than probed. The one network operation is the login,
// which plays the caps probe's role -- it is what proves the credentials and
// the tracker's reachability -- and what produces the session the search
// fan-out, the RSS poll and the download verb then carry.
//
// Every return goes through r.patch or r.finish, so every apply is the
// complete ControllerFields declaration, including on the early returns.
func (r *Reconciler) reconcileDefinition(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	conditions []metav1.Condition,
	now time.Time,
) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "indexer.reconcileDefinition")
	defer span.End()
	log := logging.FromContext(ctx).With("indexer", client.ObjectKeyFromObject(idx))

	def, err := resolveDefinition(ctx, r.Client, idx.Spec)
	if err != nil {
		tracing.RecordError(span, err)
		reason := k8s.ReasonDependencyNotReady
		switch {
		case errors.Is(err, ErrDefinitionNotFound):
			reason = ReasonDefinitionNotFound
		case errors.Is(err, errDefinitionInvalid):
			reason = ReasonDefinitionInvalid
		}
		// status.protocol, .privacy and .caps are left as they are:
		// ControllerFields re-sends them, so a definition that briefly
		// vanished does not reset what the last good one resolved to.
		k8s.MarkReady(idx, &conditions, false, reason, "%s", err.Error())
		return r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: definitionRetryInterval})
	}

	idx.Status.Protocol = definitionProtocol
	idx.Status.Privacy = definitionPrivacy[def.Type]
	caps := definitionCaps(def)
	idx.Status.Caps = &caps

	secret, err := readSecret(ctx, r.Client, idx.Namespace, idx.Spec.SecretRef)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkUnknown(idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonDependencyNotReady, "%s", err.Error())
		k8s.MarkReady(idx, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
		return r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: reprobeInterval})
	}

	applyRateLimit(idx.Spec, def, r.Limiters, definitionDelay(def.RequestDelay))

	transport, err := resolveProxy(ctx, r.Client, idx)
	if err != nil {
		return r.proxyUnavailable(ctx, idx, conditions, err)
	}

	sessions := r.sessions()
	current, err := sessions.Load(ctx, idx)
	if err != nil {
		tracing.RecordError(span, err)
		k8s.MarkReady(idx, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
		return r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: reprobeInterval})
	}

	cg, err := buildCardigann(idx.Spec, def, secret, current, r.Limiters, transport)
	if err != nil {
		// Settings that do not resolve against the definition are the
		// spec's fault; no retry fixes them.
		tracing.RecordError(span, err)
		k8s.MarkReady(idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
		if _, perr := r.patch(ctx, idx, conditions, ctrl.Result{}); perr != nil {
			return ctrl.Result{}, perr
		}
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	outcome := probeOutcome{}
	probed := false
	if r.needsLogin(idx, def, current, now) {
		probed = true
		start := time.Now()
		sess, lerr := cg.engine.Login(ctx, def, cg.cfg)
		metrics.IndexerQueryDuration.WithLabelValues(idx.Name, "login").Observe(time.Since(start).Seconds())
		outcome = classifyLogin(lerr)
		metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, outcomeLabel(outcome)).Inc()
		switch {
		case lerr != nil:
			tracing.RecordError(span, lerr)
			log.Warn("cardigann login failed", "reason", outcome.Reason, "error", cardigann.RedactErr(lerr))
		case sess != nil && def.RequiresSession():
			if serr := sessions.Save(ctx, idx, sess); serr != nil {
				// The login worked but nothing downstream can use it.
				// Reported as a failed probe so the next tick retries.
				tracing.RecordError(span, serr)
				outcome = probeOutcome{Reason: ReasonProbeFailed, Message: serr.Error()}
				break
			}
			r.markProbed(idx.UID, idx.Generation, now)
			// The cached client carries the OLD session; the key cannot see
			// a new one, so evict and let the next search rebuild.
			if r.Clients != nil {
				r.Clients.Forget(idx.UID)
			}
		default:
			r.markProbed(idx.UID, idx.Generation, now)
		}
		// The login was a network round trip, seconds long. Re-read before
		// deriving the conditions from the worker-owned fields and applying.
		if gone, err := r.refreshWorkerFields(ctx, idx); err != nil || gone {
			return ctrl.Result{}, err
		}
	}

	return r.finish(ctx, idx, conditions, probed, outcome, time.Now())
}

// needsLogin decides whether this pass logs in.
//
// A session-producing login (form, post, cookie) runs when there is no
// session or it is within sessionRenewMargin of expiring -- the fan-out never
// logs in itself, so the session must already be there when a search lands.
// A get/oneurl login produces nothing to keep and only proves the
// credentials, so it runs on the caps-probe cadence (new generation, or
// capsTTL elapsed) rather than every tick. A public definition never logs in.
func (r *Reconciler) needsLogin(idx *indexv1alpha1.Indexer, def *cardigann.Definition, current *cardigann.Session, now time.Time) bool {
	if def.Login == nil {
		return false
	}
	if def.RequiresSession() {
		return current.Expired(now.Add(sessionRenewMargin))
	}
	return r.shouldProbe(idx.UID, idx.Generation, true, now)
}

// sessions is the store this reconciler logs in to. NewReconciler builds it
// from the bus; a Reconciler built by hand in a test falls back to the owned
// Secret alone.
func (r *Reconciler) sessions() *SessionStore {
	if r.Sessions != nil {
		return r.Sessions
	}
	return NewSessionStore(r.Client, nil)
}

// proxyUnavailable reports a spec.proxyRef that cannot be routed through.
// It is not terminal -- the IndexerProxy or its Secret may be created next --
// and the Indexer is NOT probed directly in the meantime, which would leak
// the address the proxy exists to hide.
func (r *Reconciler) proxyUnavailable(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	conditions []metav1.Condition,
	err error,
) (ctrl.Result, error) {
	logging.FromContext(ctx).Warn("indexer proxy unavailable; not probing the indexer directly",
		"indexer", client.ObjectKeyFromObject(idx), "error", err)
	k8s.MarkUnknown(idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, ReasonProxyUnavailable, "%s", err.Error())
	k8s.MarkReady(idx, &conditions, false, ReasonProxyUnavailable, "%s", err.Error())
	return r.patch(ctx, idx, conditions, ctrl.Result{RequeueAfter: definitionRetryInterval})
}

// refreshWorkerFields re-reads the Indexer after a slow network operation
// and takes the WORKER-owned status fields from the live object.
//
// This reconciler applies only k8s.ManagerIndexarr's fields and is their only
// writer, so its own apply cannot roll another writer back. What it CAN do is
// derive Healthy, RateLimited, Ready and the requeue delay from a worker
// snapshot a search or an RSS poll has moved on from in the seconds a login
// or a caps probe took -- reporting Healthy=True over an indexer the fan-out
// just put into backoff, and requeueing at the wrong time. The read-slow-
// apply rule (CLAUDE.md, "A lost update is not an SSA release") applies to
// what the apply is DERIVED from as much as to what it carries.
//
// gone is true when the Indexer was deleted meanwhile; the caller returns.
func (r *Reconciler) refreshWorkerFields(ctx context.Context, idx *indexv1alpha1.Indexer) (gone bool, err error) {
	var live indexv1alpha1.Indexer
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(idx), &live); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	st := &idx.Status
	ls := live.Status
	st.EscalationLevel = ls.EscalationLevel
	st.DisabledUntil = ls.DisabledUntil
	st.InitialFailureAt = ls.InitialFailureAt
	st.LastFailureAt = ls.LastFailureAt
	st.LastFailure = ls.LastFailure
	st.QueriesInWindow = ls.QueriesInWindow
	st.GrabsInWindow = ls.GrabsInWindow
	st.LastRssAt = ls.LastRssAt
	st.LastRssNewCount = ls.LastRssNewCount
	st.IndexedReleases = ls.IndexedReleases
	return false, nil
}
