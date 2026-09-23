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

// The +kubebuilder:rbac markers for this package live in doc.go, at package
// level -- see that file for why.
package download

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	grabarrstatus "github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// clientRefIndexKey indexes Download by spec.clientRef, so a DownloadClient
// watch can find the (at most one round of) Downloads waiting on it without a
// namespace-wide List on every event.
const clientRefIndexKey = ".spec.clientRef"

// requeueWaiting is how long to wait before re-checking a dependency that has
// no watch of its own reason to fire again soon -- a DownloadClient that does
// not exist yet, or one whose EngineReady the DownloadClient watch (via
// clientRefIndexKey) will normally wake this reconcile for anyway. It is a
// safety net, not the primary wake path.
const requeueWaiting = 15 * time.Second

// Reason tokens local to Download; see pkg/k8s.Reason* for the shared set
// this reuses.
const (
	// ReasonNoEnabledClient means no enabled DownloadClient matches spec.protocol.
	ReasonNoEnabledClient = "NoEnabledClient"
	// ReasonEngineNotReady means the assigned DownloadClient's EngineReady
	// condition is not True.
	ReasonEngineNotReady = "EngineNotReady"
)

// Reconciler picks a DownloadClient for a Download, waits for its engine, pins
// the assignment, and runs the removeDataOnDelete finalizer. Field manager:
// k8s.ManagerGrabarr only; see doc.go for the full scope and the fields this
// task deliberately leaves to later work.
type Reconciler struct {
	Client   client.Client
	Recorder events.EventRecorder

	// DataDir is the shared RWX volume every grabarr pod -- controller and
	// engine alike -- mounts (config/manager/grabarr.yaml), used by the
	// finalizer to remove a Download's on-disk content directly. See doc.go's
	// "The finalizer needs no live engine".
	DataDir string
}

// NewReconciler builds a Reconciler.
func NewReconciler(c client.Client, recorder events.EventRecorder, dataDir string) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder, DataDir: dataDir}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "download.Reconciler.Reconcile")
	defer span.End()

	var dl downloadv1alpha1.Download
	if err := r.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if k8s.IsDeleting(&dl) {
		return r.reconcileDelete(ctx, &dl)
	}

	name, err := k8s.FinalizerFor(&dl, r.Client.Scheme())
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &dl, name); err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcileNormal(ctx, &dl)
}

// reconcileNormal implements the D2-4 lifecycle: pick a client once, wait for
// its engine, pin status.engine, and never recompute it afterwards.
func (r *Reconciler) reconcileNormal(ctx context.Context, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "download.Reconciler.reconcileNormal")
	defer span.End()
	log := logging.FromContext(ctx).With("download", client.ObjectKeyFromObject(dl))

	if dl.Status.Engine != "" {
		// Pinned already: doc.go's "one-way door". This reconcile has nothing
		// new to decide, but grabarr/status.ControllerFields does not seed
		// Conditions (WithConditions appends, so a seed plus a fresh set would
		// duplicate the "Assigned" entry) -- reassert it explicitly rather
		// than let an apply with no mutate silently omit it.
		if err := r.applyStatus(ctx, dl, downloadv1alpha1.DownloadPhaseAssigned, dl.Status.Engine, true,
			k8s.ReasonReconciled, "clientRef=%s engine=%s", dl.Spec.ClientRef, dl.Status.Engine); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	clientName := dl.Spec.ClientRef
	var dc downloadv1alpha1.DownloadClient
	if clientName == "" {
		var clients downloadv1alpha1.DownloadClientList
		if err := r.Client.List(ctx, &clients, client.InNamespace(dl.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("download: list DownloadClients: %w", err)
		}
		chosen, ok := pickClient(clients.Items, dl.Spec.Protocol)
		if !ok {
			log.Info("no enabled DownloadClient for protocol", "protocol", dl.Spec.Protocol)
			if err := r.applyStatus(ctx, dl, downloadv1alpha1.DownloadPhasePending, "", false,
				ReasonNoEnabledClient, "no enabled DownloadClient for protocol %q", dl.Spec.Protocol); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: requeueWaiting}, nil
		}
		dc = *chosen
		clientName = dc.Name

		acDL := downloadac.Download(dl.Name, dl.Namespace).
			WithSpec(downloadac.DownloadSpec().WithClientRef(clientName)).
			WithLabels(map[string]string{downloadv1alpha1.LabelClient: clientName})
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, acDL); err != nil {
			return ctrl.Result{}, fmt.Errorf("download: pin clientRef: %w", err)
		}
		dl.Spec.ClientRef = clientName
	} else {
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: clientName}, &dc); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("assigned DownloadClient not found", "clientRef", clientName)
				if err := r.applyStatus(ctx, dl, downloadv1alpha1.DownloadPhasePending, "", false,
					k8s.ReasonDependencyNotReady, "DownloadClient %q not found", clientName); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: requeueWaiting}, nil
			}
			return ctrl.Result{}, fmt.Errorf("download: get DownloadClient %s: %w", clientName, err)
		}
	}

	if !k8s.IsConditionTrue(dc.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady) {
		log.Info("waiting for engine to become ready", "clientRef", clientName)
		if err := r.applyStatus(ctx, dl, downloadv1alpha1.DownloadPhasePending, "", false,
			ReasonEngineNotReady, "DownloadClient %q engine is not ready", clientName); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}

	// §6.3: engine = "<client>-<hash(infoHash|guid) mod spec.replicas>",
	// computed from the desired replica count so a temporarily-down engine
	// keeps its share (k8s.HashOrdinal's own doc comment).
	ordinal := k8s.HashOrdinal(dc.Spec.Replicas, dl.Spec.Release.InfoHash, dl.Spec.Release.GUID)
	engine := fmt.Sprintf("%s-%d", clientName, ordinal)

	// This apply must restate spec.clientRef even though it is already set:
	// server-side apply replaces a manager's whole ownership set on every
	// apply rather than merging into it, so an apply from ManagerGrabarr that
	// omitted clientRef here would RELEASE it (nothing else owns it), and the
	// "release is per-leaf" hazard makes that release invisible until the
	// very next write -- which is exactly what happened here: the merged
	// object's clientRef went absent and "release identity is immutable"-
	// style CEL rule on clientRef rejected the write with "clientRef is
	// immutable once set", because a since-cleared field cannot equal its
	// former self. Every apply this manager sends against Download's main
	// resource must therefore be a complete declaration of everything it
	// owns there (clientRef, LabelClient, LabelEngine), the same discipline
	// grabarr/status applies to the status subresource.
	acLabels := downloadac.Download(dl.Name, dl.Namespace).
		WithSpec(downloadac.DownloadSpec().WithClientRef(clientName)).
		WithLabels(map[string]string{
			downloadv1alpha1.LabelClient: clientName,
			downloadv1alpha1.LabelEngine: engine,
		})
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, acLabels); err != nil {
		return ctrl.Result{}, fmt.Errorf("download: label engine assignment: %w", err)
	}

	if err := r.applyStatus(ctx, dl, downloadv1alpha1.DownloadPhaseAssigned, engine, true,
		k8s.ReasonReconciled, "clientRef=%s engine=%s", clientName, engine); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(dl, nil, "Normal", k8s.ReasonReconciled, "Assign",
			"assigned to DownloadClient %s engine %s", clientName, engine)
	}
	log.Info("assigned", "clientRef", clientName, "engine", engine)
	return ctrl.Result{}, nil
}

// applyStatus sends a complete k8s.ManagerGrabarr declaration: phase, engine
// (when non-empty) and the Assigned condition. It is the one status-writing
// path in this package so that every call site -- pending, waiting, assigned,
// reasserted -- goes through the same complete-declaration discipline.
func (r *Reconciler) applyStatus(
	ctx context.Context,
	dl *downloadv1alpha1.Download,
	phase downloadv1alpha1.DownloadPhase,
	engine string,
	assigned bool,
	reason, format string,
	args ...any,
) error {
	return grabarrstatus.Patch(ctx, r.Client, k8s.ManagerGrabarr, dl, func(ac *downloadac.DownloadStatusApplyConfiguration) {
		ac.WithPhase(phase)
		if engine != "" {
			ac.WithEngine(engine)
		}
		conditions := append([]metav1.Condition(nil), dl.Status.Conditions...)
		if assigned {
			k8s.MarkTrue(dl, &conditions, downloadv1alpha1.DownloadConditionAssigned, reason, format, args...)
		} else {
			k8s.MarkFalse(dl, &conditions, downloadv1alpha1.DownloadConditionAssigned, reason, format, args...)
		}
		ac.WithConditions(k8s.ConditionACs(conditions)...)
	})
}

// reconcileDelete runs the spec.removeDataOnDelete finalizer and, once done,
// drops the finalizer. See doc.go's "The finalizer needs no live engine" for
// why this reaches directly for fsops rather than a download.Client.
func (r *Reconciler) reconcileDelete(ctx context.Context, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "download.Reconciler.reconcileDelete")
	defer span.End()
	log := logging.FromContext(ctx).With("download", client.ObjectKeyFromObject(dl))

	name, err := k8s.FinalizerFor(dl, r.Client.Scheme())
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if !k8s.HasFinalizer(dl, name) {
		return ctrl.Result{}, nil
	}

	if ptr.Deref(dl.Spec.RemoveDataOnDelete, true) && dl.Status.OutputPath != "" {
		if err := fsops.SafeRemove(ctx, r.DataDir, dl.Status.OutputPath); err != nil {
			log.Error("remove downloaded data", "error", err, "outputPath", dl.Status.OutputPath)
			return ctrl.Result{}, fmt.Errorf("download: remove data for %s: %w", dl.Name, err)
		}
		log.Info("removed downloaded data", "outputPath", dl.Status.OutputPath)
	}

	if _, err := k8s.RemoveFinalizer(ctx, r.Client, dl, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// pickClient returns the lowest-priority enabled DownloadClient whose
// protocol matches, breaking ties on name for determinism. See doc.go's
// "ClientRef selection".
func pickClient(items []downloadv1alpha1.DownloadClient, protocol commonv1alpha1.Protocol) (*downloadv1alpha1.DownloadClient, bool) {
	var best *downloadv1alpha1.DownloadClient
	for i := range items {
		dc := &items[i]
		if dc.Spec.Protocol != protocol {
			continue
		}
		if !ptr.Deref(dc.Spec.Enabled, true) {
			continue
		}
		if best == nil ||
			dc.Spec.Priority < best.Spec.Priority ||
			(dc.Spec.Priority == best.Spec.Priority && dc.Name < best.Name) {
			best = dc
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

// mapDownloadClient enqueues every Download waiting on dc, keyed by
// clientRefIndexKey, so an EngineReady flip (or dc's deletion) wakes them
// without a poll.
func (r *Reconciler) mapDownloadClient(ctx context.Context, obj client.Object) []reconcile.Request {
	dc, ok := obj.(*downloadv1alpha1.DownloadClient)
	if !ok {
		return nil
	}
	var downloads downloadv1alpha1.DownloadList
	if err := r.Client.List(ctx, &downloads, client.InNamespace(dc.Namespace), client.MatchingFields{clientRefIndexKey: dc.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(downloads.Items))
	for _, d := range downloads.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: d.Namespace, Name: d.Name}})
	}
	return reqs
}

// engineReadyStatus projects a DownloadClient's EngineReady condition status,
// for k8s.StatusFieldChanged.
func engineReadyStatus(o client.Object) metav1.ConditionStatus {
	dc, ok := o.(*downloadv1alpha1.DownloadClient)
	if !ok {
		return ""
	}
	if c := k8s.FindCondition(dc.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady); c != nil {
		return c.Status
	}
	return ""
}

// SetupWithManager registers the Download controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &downloadv1alpha1.Download{}, clientRefIndexKey,
		func(o client.Object) []string {
			d, ok := o.(*downloadv1alpha1.Download)
			if !ok || d.Spec.ClientRef == "" {
				return nil
			}
			return []string{d.Spec.ClientRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("download").
		// GenerationChanged alone would miss the update that only sets
		// deletionTimestamp (that is a metadata change, not a spec change),
		// which would leave a deleted-with-finalizer Download stuck until an
		// unrelated event nudged it; k8s.Deleting() covers exactly that case.
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(k8s.Or(k8s.GenerationChanged(), k8s.Deleting()))).
		Watches(&downloadv1alpha1.DownloadClient{}, handler.EnqueueRequestsFromMapFunc(r.mapDownloadClient),
			builder.WithPredicates(k8s.StatusFieldChanged(engineReadyStatus))).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
