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

package indexerdefinition

import (
	"context"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// maxMessage caps what a schema error contributes to a condition message and
// to an Event. A jsonschema.ValidationError over a 1 MiB definition renders
// every failing subschema and runs to thousands of lines; metav1.Condition's
// message is capped at 32768 by the apiserver (a longer one is a rejected
// apply, not a truncated message) and an Event's at 1024. The first line or so
// names the offending keyword and pointer, which is the actionable part.
const maxMessage = 800

// Reconciler validates an IndexerDefinition's spec.yaml against the bundled
// Cardigann v11 schema and reports what it decoded.
//
// It performs no I/O beyond the apiserver: cardigann.Load is Validate plus a
// strict YAML decode, both pure. There is therefore no probe interval and no
// periodic requeue -- nothing can change but the spec, and the
// GenerationChanged predicate already delivers that.
type Reconciler struct {
	Client client.Client

	// Recorder is optional; a nil Recorder disables events rather than
	// panicking, which is what lets a unit test construct a Reconciler with
	// nothing but a client.
	Recorder events.EventRecorder
}

// NewReconciler builds a Reconciler. recorder comes from
// mgr.GetEventRecorder and writes events.k8s.io/v1 Events.
func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "indexerdefinition.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("indexerdefinition", req.Name)

	var def indexv1alpha1.IndexerDefinition
	if err := r.Client.Get(ctx, req.NamespacedName, &def); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), def.Status.Conditions...)

	// Seeded from the live status, so that the failure path below re-sends
	// every field it cannot recompute instead of releasing it.
	owned := summaryFrom(def.Status)

	parsed, err := cardigann.Load([]byte(def.Spec.YAML))
	if err != nil {
		tracing.RecordError(span, err)
		message := truncate(err.Error(), maxMessage)
		log.Warn("spec.yaml does not validate", "error", message)
		k8s.MarkFalse(&def, &conditions, indexv1alpha1.IndexerDefinitionConditionValid,
			k8s.ReasonInvalidSpec, "%s", message)
		if r.Recorder != nil {
			r.Recorder.Eventf(&def, nil, corev1.EventTypeWarning, k8s.ReasonInvalidSpec, "Reconcile",
				"spec.yaml does not validate against the bundled Cardigann v11 schema: %s", message)
		}
		// The complete owned set, NOT a conditions-only apply. The id, name,
		// caps and digest below are the last-validated ones: they describe
		// what the definition resolved to when it last parsed, and the Valid
		// condition is what says they no longer describe spec.yaml. An apply
		// that omitted them would release them, which reads as "reset to
		// zero" -- a healthy object gutted by a bad edit.
		if perr := r.patch(ctx, &def, owned, conditions); perr != nil {
			return ctrl.Result{}, perr
		}
		// Terminal: nothing but a spec change can fix an unparseable
		// document, and the GenerationChanged predicate already delivers
		// that. Requeueing would spin against the schema forever.
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	owned = summarise(parsed, def.Spec.YAML)
	k8s.MarkTrue(&def, &conditions, indexv1alpha1.IndexerDefinitionConditionValid,
		k8s.ReasonReconciled, "validated against the bundled Cardigann v11 schema")
	log.Debug("definition validated", "id", owned.ID, "modes", len(owned.Modes), "categories", len(owned.Categories))

	if err := r.patch(ctx, &def, owned, conditions); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// patch is the only status write in this package, so that "declare the
// complete owned set" is enforced in one place rather than remembered at four
// call sites.
func (r *Reconciler) patch(
	ctx context.Context,
	def *indexv1alpha1.IndexerDefinition,
	s summary,
	conditions []metav1.Condition,
) error {
	// IndexerDefinition is cluster-scoped: the apply configuration takes a
	// name and no namespace.
	ac := indexac.IndexerDefinition(def.Name).WithStatus(statusFor(def.Generation, s, conditions))
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerIndexarr, ac); err != nil {
		logging.FromContext(ctx).Error("patch status", "error", err)
		return err
	}
	return nil
}

// truncate shortens s to at most n bytes, backing up to a rune boundary so a
// multi-byte sequence is never cut in half -- an invalid UTF-8 byte in a
// condition message is rejected by the apiserver, which would turn a report
// about a bad definition into a failed status write.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// +kubebuilder:rbac markers for this package live in doc.go, at package level.
// controller-gen collects them only from a comment group that is not attached
// to a declaration, and a block placed here, above SetupWithManager, becomes
// that function's doc comment and is silently discarded -- which is exactly
// how four Phase C controllers shipped with no permissions at all.

// SetupWithManager registers the IndexerDefinition controller. Task D1-8 calls
// NewReconciler(...).SetupWithManager(mgr); see doc.go for the exact call.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("indexerdefinition").
		For(&indexv1alpha1.IndexerDefinition{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
