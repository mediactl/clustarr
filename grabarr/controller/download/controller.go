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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/engine"
	grabarrstatus "github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
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
	// ReasonEncrypted is the Failed condition's reason when status.isEncrypted
	// is true -- see phase.go's derivePhase for why this is currently the
	// only failure signal this package can read without a live
	// download.Client.
	ReasonEncrypted = "Encrypted"
)

// Reconciler picks a DownloadClient for a Download, waits for its engine, pins
// the assignment, advances status.phase from engine-owned telemetry once
// assigned, publishes the file-import work item, and runs the
// removeDataOnDelete finalizer. Field manager: k8s.ManagerGrabarr only; see
// doc.go for the full scope and the fields this package deliberately leaves
// to later work.
type Reconciler struct {
	Client   client.Client
	Recorder k8sevents.EventRecorder

	// DataDir is the shared RWX volume every grabarr pod -- controller and
	// engine alike -- mounts (config/manager/grabarr.yaml), used by the
	// finalizer to remove a Download's on-disk content directly. See doc.go's
	// "The finalizer needs no live engine".
	DataDir string

	// Bus publishes schema.ImportTask once a Download's content is first
	// observed complete on disk (see [derivePhase] and [publishImportTask]).
	// events.Publisher, not the full events.Bus, is deliberate: this
	// reconciler only ever produces, never subscribes or reads KV, the same
	// minimal-footprint choice catalogarr/controller/movie.Reconciler and
	// catalogarr/controller/search.Reconciler already make for their own Bus
	// fields.
	//
	// Nil is accepted by NewReconciler -- every test built before this task
	// exercises only Stage="" (Phase=Assigned), which never reaches the
	// publish path -- but it is not a silently-degraded configuration once a
	// Download's content genuinely completes: [publishImportTask] returns an
	// error rather than skipping the publish, so a misconfigured deployment
	// fails its reconcile loudly and keeps retrying rather than leaving a
	// completed Download that is never imported. Task D2-8 must wire a real
	// events.Bus before registering this controller.
	Bus events.Publisher

	// Now is the clock. Nil means time.Now; the same seam
	// downloadclient.BlocklistSweeper.Now provides, here for
	// status.startedAt/status.completedAt and the engine teardown timeout.
	Now func() time.Time

	// EngineTeardownTimeout overrides [DefaultEngineTeardownTimeout].
	EngineTeardownTimeout time.Duration
}

// NewReconciler builds a Reconciler with no Bus configured; set the field
// directly for a caller that needs status.phase to advance past
// Completed/Seeding, which is every caller other than this package's own
// pre-D2-8a fixtures.
func NewReconciler(c client.Client, recorder k8sevents.EventRecorder, dataDir string) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder, DataDir: dataDir}
}

// now returns r.Now() if set, else time.Now.
func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
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
		// Pinned already: doc.go's "one-way door". clientRef/engine are not
		// recomputed, but status.phase now is -- see advancePhase and
		// [derivePhase] (D2-8a).
		return r.advancePhase(ctx, dl)
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

	// §6.3: "Phase=Assigned; evt.download.queued". Published before the
	// apply that records it -- see publishDownloadEvent.
	r.publishDownloadEvent(ctx, dl, events.ActionQueued, "")
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
		// Every status apply this manager makes folds the DLQ projector's
		// annotation (pkg/k8s.MarkDeadLettered), the pre-assignment waits
		// included, so no apply releases the condition another one set.
		k8s.MarkDeadLettered(dl, &conditions)
		ac.WithConditions(k8s.ConditionACs(conditions)...)
	})
}

// advancePhase runs once status.engine is pinned (controller.go's "one-way
// door"). It maps engine-owned telemetry plus the Download's own history
// onto status.phase (see [derivePhase] in phase.go for the mapping and
// plan ruling R1), and publishes importarr's file-import work item exactly
// once, the first reconcile that observes the content complete on disk.
func (r *Reconciler) advancePhase(ctx context.Context, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "download.Reconciler.advancePhase")
	defer span.End()
	log := logging.FromContext(ctx).With("download", client.ObjectKeyFromObject(dl))

	res := derivePhase(dl)
	wasComplete := k8s.IsConditionTrue(dl.Status.Conditions, downloadv1alpha1.DownloadConditionDownloaded)
	nowComplete := isContentComplete(res.phase)

	// Publish BEFORE recording, never after: a crash between the two leaves
	// a Download whose content is complete but whose Downloaded condition is
	// still False, so the next reconcile (this same watch will fire again on
	// its own retry) publishes again -- survivable, because D2-7's
	// file-import worker is idempotent both through its own dedup
	// fingerprint and through its status.import.state check
	// (importarr/worker/fileimport/dedup.go, worker.go). Recording first and
	// publishing second would risk the opposite outcome: a Download marked
	// Downloaded whose import task was never actually sent, which nothing
	// in the system would ever retry.
	if nowComplete && !wasComplete {
		if err := r.publishImportTask(ctx, dl); err != nil {
			return ctrl.Result{}, fmt.Errorf("download: publish import task for %s/%s: %w", dl.Namespace, dl.Name, err)
		}
		log.Info("content complete on disk; published import task", "phase", res.phase)
	}

	// History events for the edges this reconcile observes, each on the
	// same condition the status apply below uses to record it -- see
	// publishDownloadEvent for why before, and why best effort.
	if dl.Status.StartedAt == nil && dl.Status.Stage != "" {
		r.publishDownloadEvent(ctx, dl, events.ActionStarted, "")
	}
	if nowComplete && !wasComplete {
		r.publishDownloadEvent(ctx, dl, events.ActionCompleted, "")
	}
	if action, ok := phaseActions[res.phase]; ok && res.phase != dl.Status.Phase {
		r.publishDownloadEvent(ctx, dl, action, string(res.failureReason))
	}

	if err := r.applyAdvancedStatus(ctx, dl, res, wasComplete || nowComplete); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// applyAdvancedStatus sends the complete k8s.ManagerGrabarr declaration for
// res: phase, failureReason (set only for Failed and explicitly cleared
// otherwise -- grabarr/status.ControllerFields' "to CLEAR a field" note),
// the startedAt/completedAt transition timestamps derived from this same
// phase edge, and every condition this task computes, derived fresh rather
// than carried forward (grabarr/status.ControllerFields' own doc comment:
// "the reconciler derives all five on every pass" -- SeedGoalMet is the one
// exception, deliberately not computed here; see phase.go). downloaded is
// passed in rather than recomputed so advancePhase's publish gate and the
// Downloaded condition this method sets can never disagree about whether
// content is complete.
func (r *Reconciler) applyAdvancedStatus(
	ctx context.Context,
	dl *downloadv1alpha1.Download,
	res phaseResult,
	downloaded bool,
) error {
	now := metav1.NewTime(r.now())
	return grabarrstatus.Patch(ctx, r.Client, k8s.ManagerGrabarr, dl, func(ac *downloadac.DownloadStatusApplyConfiguration) {
		ac.WithPhase(res.phase)
		ac.WithEngine(dl.Status.Engine)

		if res.phase == downloadv1alpha1.DownloadPhaseFailed {
			ac.WithFailureReason(res.failureReason)
		} else {
			// Clear rather than carry forward: ControllerFields seeds
			// FailureReason from the live status, and a Download that is no
			// longer Failed must not keep explaining a failure that is no
			// longer true.
			ac.FailureReason = nil
		}

		if dl.Status.StartedAt == nil && dl.Status.Stage != "" {
			ac.WithStartedAt(now)
		}
		if dl.Status.CompletedAt == nil && downloaded {
			ac.WithCompletedAt(now)
		}

		conditions := append([]metav1.Condition(nil), dl.Status.Conditions...)
		k8s.MarkTrue(dl, &conditions, downloadv1alpha1.DownloadConditionAssigned, k8s.ReasonReconciled,
			"clientRef=%s engine=%s", dl.Spec.ClientRef, dl.Status.Engine)
		if downloaded {
			k8s.MarkTrue(dl, &conditions, downloadv1alpha1.DownloadConditionDownloaded, k8s.ReasonSucceeded,
				"content complete on disk (phase=%s)", res.phase)
		} else {
			k8s.MarkFalse(dl, &conditions, downloadv1alpha1.DownloadConditionDownloaded, k8s.ReasonReconciling,
				"transfer in progress (stage=%s)", dl.Status.Stage)
		}
		if res.phase == downloadv1alpha1.DownloadPhaseFailed {
			// ReasonEncrypted today: it is the only failure this function
			// can reach. See phase.go for why the others are not attempted.
			k8s.MarkTrue(dl, &conditions, downloadv1alpha1.DownloadConditionFailed, ReasonEncrypted, "%s", res.failureReason)
		} else {
			k8s.MarkFalse(dl, &conditions, downloadv1alpha1.DownloadConditionFailed, k8s.ReasonSucceeded, "no failure observed")
		}
		if res.phase == downloadv1alpha1.DownloadPhaseImported {
			k8s.MarkTrue(dl, &conditions, downloadv1alpha1.DownloadConditionImported, k8s.ReasonSucceeded, "import finished")
		} else {
			k8s.MarkFalse(dl, &conditions, downloadv1alpha1.DownloadConditionImported, k8s.ReasonReconciling, "not yet imported")
		}
		k8s.MarkDeadLettered(dl, &conditions)
		ac.WithConditions(k8s.ConditionACs(conditions)...)
	})
}

// publishImportTask publishes schema.ImportTask so importarr's
// ConsumerImportFile file-import worker (D2-7) imports dl. advancePhase
// calls it at most once per completion via its Downloaded-condition gate;
// this method adds a second, independent idempotency layer for the one case
// that gate cannot cover -- a crash between this publish succeeding and the
// status apply that records it (grabarr/status.Patch, in
// applyAdvancedStatus), which would otherwise leave the next reconcile
// reading a still-False Downloaded condition and publishing a duplicate.
// The Envelope's ID is deterministic per Download identity rather than per
// publish attempt, so the broker's own MsgID dedup window absorbs that
// specific near-term retry; D2-7's worker is also independently idempotent
// through its own dedup fingerprint (importarr/worker/fileimport/dedup.go)
// and its status.import.state check, so a duplicate that outlives both
// windows is survivable rather than free -- exactly what the plan asks the
// producer to be.
func (r *Reconciler) publishImportTask(ctx context.Context, dl *downloadv1alpha1.Download) error {
	if r.Bus == nil {
		return fmt.Errorf("download: no event bus configured for %s/%s", dl.Namespace, dl.Name)
	}

	task := schema.ImportTask{
		DownloadRef: schema.Ref{Namespace: dl.Namespace, Name: dl.Name, UID: string(dl.UID)},
	}
	schemaName, data, err := schema.Encode(task)
	if err != nil {
		return fmt.Errorf("encode import task: %w", err)
	}
	subject := events.WorkFileImportSubject(string(dl.UID))
	env := &events.Envelope{
		ID:     dl.Namespace + "/" + dl.Name + ":" + string(dl.UID) + ":import",
		Type:   "catalog.ImportTask",
		Schema: schemaName,
		Source: "grabarr-controller@" + version.String(),
		Key:    dl.Namespace + "/" + dl.Name,
		Time:   r.now(),
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// reconcileDelete runs the spec.removeDataOnDelete finalizer and, once done,
// drops the finalizer. It is the controller's half of the teardown protocol
// (ruling R-6; grabarr/engine's package doc): it removes status.outputPath
// only once the engine has dropped grabarr/engine's finalizer -- i.e. once
// the engine has removed the transfer and released its files -- so it no
// longer unlinks files an engine is still writing or seeding.
//
// A live engine is waited for without a bound: it acts on the deletion the
// moment its watch delivers it, and its finalizer coming off wakes this
// reconcile (the Deleting predicate passes any update to a deleting
// object). An engine that is GONE ([Reconciler.engineGone]) is waited for
// [DefaultEngineTeardownTimeout] from the deletionTimestamp, in case it is
// only restarting; after that this controller drops the engine finalizer
// on its behalf, with a Warning Event, and the engine's orphan reaper
// clears the transfer if the engine ever comes back.
//
// The data volume is shared (see doc.go's "The finalizer needs no live
// engine"), so the removal itself still needs no engine. An outputPath the
// engine already removed (it honours removeDataOnDelete too) is skipped.
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

	if k8s.HasFinalizer(dl, engine.Finalizer) {
		gone, why, err := r.engineGone(ctx, dl)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !gone {
			log.Info("waiting for the engine to release the transfer", "engine", dl.Status.Engine)
			return ctrl.Result{RequeueAfter: requeueWaiting}, nil
		}
		timeout := r.engineTeardownTimeout()
		if waited := r.now().Sub(dl.DeletionTimestamp.Time); waited < timeout {
			log.Info("engine is gone; waiting out the teardown timeout before releasing it",
				"engine", dl.Status.Engine, "why", why, "remaining", timeout-waited)
			return ctrl.Result{RequeueAfter: min(timeout-waited, requeueWaiting)}, nil
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(dl, nil, "Warning", ReasonEngineGone, "Finalize",
				"engine %s did not release the transfer within %s (%s); releasing it on the engine's behalf",
				dl.Status.Engine, timeout, why)
		}
		log.Warn("releasing the engine finalizer on a gone engine's behalf", "engine", dl.Status.Engine, "why", why)
		if _, err := k8s.RemoveFinalizer(ctx, r.Client, dl, engine.Finalizer); err != nil {
			return ctrl.Result{}, err
		}
	}

	removeData := ptr.Deref(dl.Spec.RemoveDataOnDelete, true)
	if removeData && dl.Status.OutputPath != "" {
		if _, statErr := os.Lstat(dl.Status.OutputPath); errors.Is(statErr, fs.ErrNotExist) {
			log.Info("downloaded data already gone", "outputPath", dl.Status.OutputPath)
		} else {
			if err := fsops.SafeRemove(ctx, r.DataDir, dl.Status.OutputPath); err != nil {
				log.Error("remove downloaded data", "error", err, "outputPath", dl.Status.OutputPath)
				return ctrl.Result{}, fmt.Errorf("download: remove data for %s: %w", dl.Name, err)
			}
			log.Info("removed downloaded data", "outputPath", dl.Status.OutputPath)
		}
	}

	reason := "removeDataOnDelete=false"
	if removeData {
		reason = "removeDataOnDelete=true"
	}
	r.publishDownloadEvent(ctx, dl, events.ActionRemoved, reason)

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

// phaseSignal projects the Download fields [derivePhase] reads that are NOT
// covered by metadata.generation, for k8s.StatusFieldChanged on the
// controller's own watch of Download. Without this, D2-8a's phase
// advancement would never fire: status.stage and status.isEncrypted are
// written by k8s.ManagerGrabarrEngine and status.import by
// k8s.ManagerImportarr, on the SAME object this controller already watches,
// but neither write bumps metadata.generation (only a spec change does),
// and a label add/change -- the blocklist path -- bumps neither generation
// nor any status field. Every existing predicate on this watch
// (GenerationChanged, Deleting) would silently miss all three, which is
// exactly the class of bug the plan's own hazard list calls a lost wake
// rather than a lost update: the reconcile that would have advanced the
// phase simply never runs.
//
// status.progressPercent and the byte/rate counters are deliberately absent
// from the projection: [derivePhase] does not read them (see phase.go), so
// including them here would only turn this controller into the
// continuously-reconciling hot loop predicates.go's own package comment
// warns against ("a metadata refresh ... must not wake squasharr").
type downloadPhaseSignal struct {
	stage       downloadv1alpha1.DownloadStage
	encrypted   bool
	importDone  bool
	blocklisted bool
}

func phaseSignal(o client.Object) downloadPhaseSignal {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return downloadPhaseSignal{}
	}
	return downloadPhaseSignal{
		stage:       dl.Status.Stage,
		encrypted:   dl.Status.IsEncrypted,
		importDone:  dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported,
		blocklisted: dl.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue,
	}
}

// downloadPredicate is the controller's own watch filter on Download.
// GenerationChanged alone would miss the update that only sets
// deletionTimestamp (a metadata change, not a spec change) -- k8s.Deleting()
// covers that, and also wakes the finalizer when an engine drops
// grabarr/engine's finalizer. k8s.StatusFieldChanged(phaseSignal) is D2-8a's:
// see phaseSignal for why phase advancement is unreachable without it.
// k8s.DeadLetteredAnnotationChanged() is the DLQ fold's: the projector's
// annotation, and an operator removing it, touch neither generation nor any
// status field.
func downloadPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(), k8s.Deleting(), k8s.StatusFieldChanged(phaseSignal),
		k8s.DeadLetteredAnnotationChanged())
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
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(downloadPredicate())).
		Watches(&downloadv1alpha1.DownloadClient{}, handler.EnqueueRequestsFromMapFunc(r.mapDownloadClient),
			builder.WithPredicates(k8s.StatusFieldChanged(engineReadyStatus))).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
