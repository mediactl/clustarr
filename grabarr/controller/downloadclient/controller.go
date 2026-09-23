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
package downloadclient

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// recheckInterval is how often a DownloadClient is re-reconciled with no spec
// change or child-workload event to prompt it -- the same invented, documented
// default catalogarr/controller/rootfolder uses for the same reason: DiskSpaceOK
// is a live filesystem fact that can change with nothing in the cluster telling
// this controller so.
const recheckInterval = 5 * time.Minute

// Reason tokens local to DownloadClient; see pkg/k8s.Reason* for the shared
// set this reuses.
const (
	// ReasonBelowMinFree means DataDir's free space is under MinFreeBytes.
	ReasonBelowMinFree = "BelowMinFreeBytes"
	// ReasonStatfsFailed means the DiskUsage probe itself returned an error.
	ReasonStatfsFailed = "StatfsFailed"
	// ReasonEngineNotReady means the owned workload has fewer ready replicas
	// than desired.
	ReasonEngineNotReady = "EngineNotReady"
)

// Reconciler stands up a DownloadClient's engine workload -- a StatefulSet for
// protocol=torrent, a Deployment for protocol=usenet -- and reports
// DiskSpaceOK, EngineReady and the Ready roll-up. Field manager: k8s.ManagerGrabarr
// only; see doc.go for why the engine's own field set (k8s.ManagerGrabarrEngine,
// on Download.status) is out of scope here.
type Reconciler struct {
	Client   client.Client
	Recorder events.EventRecorder

	// DataDir is the shared RWX volume every grabarr pod mounts at /data
	// (config/manager/grabarr.yaml). DiskSpaceOK statfs's it directly on the
	// CONTROLLER pod, which is why the controller Deployment mounts the same
	// claim the engines do rather than this reconciler needing to ask an
	// engine pod for its own view.
	DataDir string

	// ScratchDir is where a usenet engine's scratch volume is mounted inside
	// the pod. It has no bearing on DiskSpaceOK (that is always DataDir,
	// because DataDir is where the finished content ends up for both
	// protocols) -- it only sizes the usenet container's volume mount.
	ScratchDir string

	// EngineImage is the container image stamped onto the engine workload.
	// There is no default: guessing an image tag would silently run the wrong
	// engine. D2-8 reads CLUSTARR_ENGINE_IMAGE and passes it here.
	EngineImage string

	// DataClaimName is the PersistentVolumeClaim every grabarr pod mounts at
	// DataDir. The two installers do NOT agree on it: config/ names it
	// "clustarr-data" (DefaultDataClaimName, which NewReconciler sets), while
	// charts/clustarr names it "<release fullname>-data". grabarr/run.go
	// therefore always overwrites it from --data-claim ($CLUSTARR_DATA_CLAIM,
	// which the chart sets); this field once claimed both installers used
	// "clustarr-data", and under any release name but "clustarr" every
	// engine mounted a claim that did not exist.
	DataClaimName string

	// Engine is what every engine pod needs from this controller's own
	// process: its ServiceAccount, the bus, the umask (see EngineRuntime).
	// NewReconciler sets Engine.ServiceAccountName to
	// [DefaultEngineServiceAccount]; grabarr/run.go overwrites every field
	// from its options and environment.
	Engine EngineRuntime

	// MinFreeBytes is the floor DiskSpaceOK enforces on DataDir. Zero uses
	// [DefaultMinFreeBytes].
	MinFreeBytes int64

	// DiskUsage is the filesystem probe; production uses fsops.DiskUsage.
	DiskUsage diskUsageFunc
}

// DefaultDataClaimName is the PVC config/'s grabarr pods -- controller and
// engine alike -- mount at DataDir, per config/manager/grabarr.yaml. The
// chart's differs; see Reconciler.DataClaimName.
const DefaultDataClaimName = "clustarr-data"

// NewReconciler builds a Reconciler with the real filesystem probe and the
// project's documented defaults.
func NewReconciler(c client.Client, recorder events.EventRecorder, dataDir, scratchDir, engineImage string) *Reconciler {
	return &Reconciler{
		Client:        c,
		Recorder:      recorder,
		DataDir:       dataDir,
		ScratchDir:    scratchDir,
		EngineImage:   engineImage,
		DataClaimName: DefaultDataClaimName,
		Engine:        EngineRuntime{ServiceAccountName: DefaultEngineServiceAccount},
		MinFreeBytes:  DefaultMinFreeBytes,
		DiskUsage:     fsops.DiskUsage,
	}
}

func (r *Reconciler) minFreeBytes() int64 {
	if r.MinFreeBytes > 0 {
		return r.MinFreeBytes
	}
	return DefaultMinFreeBytes
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "downloadclient.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("downloadclient", req.NamespacedName)

	var dc downloadv1alpha1.DownloadClient
	if err := r.Client.Get(ctx, req.NamespacedName, &dc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ownerRef, err := k8s.OwnerReferenceAC(&dc, r.Client.Scheme())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("downloadclient: owner reference: %w", err)
	}

	workloadName := engineWorkloadName(dc.Name)
	desiredReplicas, replicas, readyReplicas, err := r.reconcileWorkload(ctx, &dc, workloadName, ownerRef)
	if err != nil {
		log.Error("reconcile engine workload", "error", err)
		return ctrl.Result{}, err
	}

	active, queued, seeding, downRate, upRate, err := r.aggregateDownloads(ctx, &dc)
	if err != nil {
		log.Error("aggregate downloads", "error", err)
		return ctrl.Result{}, err
	}

	usage, usageErr := r.DiskUsage(r.DataDir)
	diskOK := usageErr == nil && usage.Free >= r.minFreeBytes()

	conditions := append([]metav1.Condition(nil), dc.Status.Conditions...)
	setDiskSpaceCondition(&dc, &conditions, diskOK, usage.Free, r.minFreeBytes(), usageErr)
	engineReady := desiredReplicas > 0 && replicas == desiredReplicas && readyReplicas == desiredReplicas
	setEngineReadyCondition(&dc, &conditions, engineReady, replicas, readyReplicas, desiredReplicas)

	ready := diskOK && engineReady
	reason, message := k8s.ReasonReconciled, "engine ready and disk space above minFreeBytes"
	if !ready {
		reason = ReasonEngineNotReady
		message = "engine or disk space not ready; see EngineReady and DiskSpaceOK"
		if !diskOK {
			reason = ReasonBelowMinFree
		}
	}
	if k8s.MarkReady(&dc, &conditions, ready, reason, "%s", message) && !ready && r.Recorder != nil {
		r.Recorder.Eventf(&dc, nil, "Warning", reason, "Reconcile", message)
	}

	ac := downloadac.DownloadClient(dc.Name, dc.Namespace).WithStatus(
		downloadac.DownloadClientStatus().
			WithObservedGeneration(dc.Generation).
			WithEngine(downloadac.EngineStatus().
				WithWorkloadRef(workloadName).
				WithReplicas(replicas).
				WithReadyReplicas(readyReplicas)).
			WithActive(active).
			WithQueued(queued).
			WithSeeding(seeding).
			WithDownloadRateBps(downRate).
			WithUploadRateBps(upRate).
			WithFreeBytes(usage.Free).
			WithConditions(k8s.ConditionACs(conditions)...),
	)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerGrabarr, ac); err != nil {
		log.Error("patch status", "error", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: recheckInterval}, nil
}

// recreateStrategyPatch moves a Deployment to the Recreate strategy and drops
// the rollingUpdate block with it -- see [Reconciler.migrateToRecreate].
var recreateStrategyPatch = []byte(`{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}`)

// migrateToRecreate moves a usenet engine Deployment created before
// buildDeployment set the Recreate strategy onto it, once, ahead of the
// apply. Server-side apply cannot do this itself: the old Deployment carries
// the apiserver's defaulted rollingUpdate block, which no field manager owns
// and an apply therefore never removes, and the apiserver rejects a Recreate
// Deployment that still has one -- so the apply would fail on every reconcile
// from then on. A merge patch can say "rollingUpdate: null"; an apply
// configuration cannot. A Deployment that is absent or already Recreate is
// left alone, so this costs one cached Get in the steady state.
func (r *Reconciler) migrateToRecreate(ctx context.Context, namespace, name string) error {
	var live appsv1.Deployment
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("downloadclient: get Deployment %s: %w", name, err)
	}
	if live.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return nil
	}
	if err := r.Client.Patch(ctx, &live, client.RawPatch(types.MergePatchType, recreateStrategyPatch),
		client.FieldOwner(string(k8s.ManagerGrabarr))); err != nil {
		return fmt.Errorf("downloadclient: move Deployment %s to the Recreate strategy: %w", name, err)
	}
	return nil
}

// reconcileWorkload applies the StatefulSet (torrent) or Deployment (usenet)
// the DownloadClient describes and reads back its live replica counts. It
// returns the desired replica count alongside the two observed ones so the
// caller can compute EngineReady without a second round trip.
func (r *Reconciler) reconcileWorkload(
	ctx context.Context,
	dc *downloadv1alpha1.DownloadClient,
	workloadName string,
	ownerRef *metav1ac.OwnerReferenceApplyConfiguration,
) (desired, replicas, readyReplicas int32, err error) {
	switch dc.Spec.Protocol {
	case commonv1alpha1.ProtocolTorrent:
		sts := buildStatefulSet(dc, workloadName, r.EngineImage, r.DataDir, r.DataClaimName, r.Engine, ownerRef)
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, sts); err != nil {
			return 0, 0, 0, fmt.Errorf("downloadclient: apply StatefulSet %s: %w", workloadName, err)
		}
		var live appsv1.StatefulSet
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: workloadName}, &live); err != nil {
			if apierrors.IsNotFound(err) {
				return torrentReplicas(dc), 0, 0, nil
			}
			return 0, 0, 0, fmt.Errorf("downloadclient: get StatefulSet %s: %w", workloadName, err)
		}
		return torrentReplicas(dc), live.Status.Replicas, live.Status.ReadyReplicas, nil

	case commonv1alpha1.ProtocolUsenet:
		if dc.Spec.Usenet != nil && dc.Spec.Usenet.Scratch != nil && dc.Spec.Usenet.Scratch.StorageClassName != nil {
			pvc := buildScratchPVC(dc, ownerRef)
			if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, pvc); err != nil {
				return 0, 0, 0, fmt.Errorf("downloadclient: apply scratch PVC for %s: %w", dc.Name, err)
			}
		}
		if err := r.migrateToRecreate(ctx, dc.Namespace, workloadName); err != nil {
			return 0, 0, 0, err
		}
		dep := buildDeployment(dc, workloadName, r.EngineImage, r.DataDir, r.ScratchDir, r.DataClaimName, r.Engine, ownerRef)
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, dep); err != nil {
			return 0, 0, 0, fmt.Errorf("downloadclient: apply Deployment %s: %w", workloadName, err)
		}
		var live appsv1.Deployment
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: workloadName}, &live); err != nil {
			if apierrors.IsNotFound(err) {
				return 1, 0, 0, nil
			}
			return 0, 0, 0, fmt.Errorf("downloadclient: get Deployment %s: %w", workloadName, err)
		}
		return 1, live.Status.Replicas, live.Status.ReadyReplicas, nil

	default:
		// DownloadClientSpec's own CEL rule requires exactly one of torrent or
		// usenet, keyed off spec.protocol's enum, so this is unreachable
		// through the apiserver. It is left unhandled -- no workload applied,
		// no status touched -- rather than sending a partial status
		// declaration for a case that cannot legitimately arise: an apply
		// nobody ever needs to send cannot release a field, but one built out
		// of a defensive guess could.
		return 0, 0, 0, fmt.Errorf("downloadclient: unknown protocol %q", dc.Spec.Protocol)
	}
}

// aggregateDownloads rolls up every Download labelled for this client into
// the three counters and two rates DownloadClientStatus reports. The mapping
// from DownloadPhase to these buckets is this task's own reading of each
// field's doc comment (DownloadClientStatus's Active/Queued/Seeding), not a
// contract fixed elsewhere: Assigned and Queued are both "has a client, not
// yet transferring" so both count as queued; Downloading is active; Seeding
// is seeding. Every other phase (Pending has no client yet so is never
// labelled for one; Paused, Completed, Imported, Failed, Blocklisted,
// Removing) counts toward none of the three, which matches each field's own
// wording ("currently transferring", "not yet transferring", "seeding").
func (r *Reconciler) aggregateDownloads(ctx context.Context, dc *downloadv1alpha1.DownloadClient) (active, queued, seeding int32, downRate, upRate int64, err error) {
	var downloads downloadv1alpha1.DownloadList
	if err := r.Client.List(ctx, &downloads, client.InNamespace(dc.Namespace), client.MatchingLabels{downloadv1alpha1.LabelClient: dc.Name}); err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("downloadclient: list Downloads for %s: %w", dc.Name, err)
	}
	for i := range downloads.Items {
		d := &downloads.Items[i]
		switch d.Status.Phase {
		case downloadv1alpha1.DownloadPhaseAssigned, downloadv1alpha1.DownloadPhaseQueued:
			queued++
		case downloadv1alpha1.DownloadPhaseDownloading:
			active++
			downRate += d.Status.DownloadRateBps
			upRate += d.Status.UploadRateBps
		case downloadv1alpha1.DownloadPhaseSeeding:
			seeding++
			upRate += d.Status.UploadRateBps
		}
	}
	return active, queued, seeding, downRate, upRate, nil
}

func setDiskSpaceCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, ok bool, free, minFree int64, probeErr error) {
	if probeErr != nil {
		k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, ReasonStatfsFailed, "%s", probeErr.Error())
		return
	}
	if ok {
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, k8s.ReasonReconciled,
			"free=%d minFreeBytes=%d", free, minFree)
		return
	}
	k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, ReasonBelowMinFree,
		"free=%d minFreeBytes=%d", free, minFree)
}

func setEngineReadyCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, ready bool, replicas, readyReplicas, desired int32) {
	if ready {
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionEngineReady, k8s.ReasonReconciled,
			"%d/%d replicas ready", readyReplicas, desired)
		return
	}
	k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionEngineReady, ReasonEngineNotReady,
		"%d/%d replicas exist, %d ready, want %d", replicas, desired, readyReplicas, desired)
}

// mapDownloadToClient enqueues the DownloadClient a Download is labelled for,
// so an assignment or a phase change is reconciled promptly instead of
// waiting up to [recheckInterval] for the Active/Queued/Seeding rollup to
// catch up.
func mapDownloadToClient(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[downloadv1alpha1.LabelClient]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
}

// SetupWithManager registers the DownloadClient controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("downloadclient").
		For(&downloadv1alpha1.DownloadClient{}, builder.WithPredicates(k8s.GenerationChanged())).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(mapDownloadToClient)).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
