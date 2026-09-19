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

package rootfolder

import (
	"context"
	"fmt"
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
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// recheckInterval is how often an already-Ready RootFolder is re-probed for
// free space between spec changes. Not given by the spec; invented here as a
// cheap-enough, frequent-enough default -- one statfs call every 5 minutes
// catches a filling disk long before it is disastrous. Revisit if a later
// task adds a cheaper signal (e.g. a kubelet volume metric).
const recheckInterval = 5 * time.Minute

// Reason tokens local to RootFolder; see pkg/k8s.Reason* for the shared set
// this reuses (ReasonReconciled, ReasonReconcileError).
const (
	ReasonPathNotFound = "PathNotFound"
	ReasonNotWritable  = "NotWritable"
	ReasonBelowMinFree = "BelowMinFreeBytes"
)

// Reconciler resolves a RootFolder's accessibility and free space.
type Reconciler struct {
	Client   client.Client
	Recorder events.EventRecorder

	// CheckPath is the filesystem probe. Production uses checkPath (probe.go);
	// tests inject a fake so they never need a real /data/media/... path.
	CheckPath func(path string) (accessible bool, freeBytes, totalBytes int64, err error)
}

// NewReconciler builds a Reconciler with the real filesystem probe.
func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder, CheckPath: checkPath}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "rootfolder.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("rootfolder", req.NamespacedName)

	var rf catalogv1alpha1.RootFolder
	if err := r.Client.Get(ctx, req.NamespacedName, &rf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), rf.Status.Conditions...)
	accessible, free, total, err := r.CheckPath(rf.Spec.Path)

	var reason, message string
	switch {
	case err != nil:
		reason, message = ReasonPathNotFound, err.Error()
		if accessible {
			// checkPath found the path but a later step (DiskUsage) failed;
			// keep the specific reason but do not claim NotWritable.
			reason = ReasonNotWritable
		}
	case !accessible:
		reason, message = ReasonNotWritable, "path exists but is not writable"
	}

	diskOK := accessible && err == nil && fsops.EnsureFreeSpace(rf.Spec.Path, rf.Spec.MinFreeBytes) == nil
	if accessible && err == nil {
		if diskOK {
			k8s.MarkTrue(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, k8s.ReasonReconciled, "free space above minFreeBytes")
		} else {
			// The path itself is fine; only the free-space check failed. reason
			// and message were left empty by the switch above (it only covers
			// the "not accessible at all" cases), so set them here too --
			// otherwise the Ready condition below would be marked False with
			// an empty reason, which the CRD's schema rejects.
			reason = ReasonBelowMinFree
			message = fmt.Sprintf("free=%d minFreeBytes=%d", free, rf.Spec.MinFreeBytes)
			k8s.MarkFalse(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, reason, "%s", message)
		}
	} else {
		k8s.MarkFalse(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, reason, "%s", message)
	}

	ready := accessible && err == nil && diskOK
	if ready {
		k8s.MarkReady(&rf, &conditions, true, k8s.ReasonReconciled, "path is accessible with sufficient free space")
	} else {
		k8s.MarkReady(&rf, &conditions, false, reason, "%s", message)
		if r.Recorder != nil {
			r.Recorder.Eventf(&rf, nil, "Warning", reason, "Reconcile", message)
		}
	}

	ac := catalogac.RootFolder(rf.Name, rf.Namespace).WithStatus(
		catalogac.RootFolderStatus().
			WithObservedGeneration(rf.Generation).
			WithAccessible(accessible && err == nil).
			WithFreeBytes(free).
			WithTotalBytes(total).
			WithConditions(k8s.ConditionACs(conditions)...),
	)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
		log.Error("patch status", "error", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: recheckInterval}, nil
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders/status,verbs=get;update;patch
// The Recorder here is a k8s.io/client-go/tools/events.EventRecorder, so
// events.k8s.io is the correct group -- unlike the Movie, Series, Episode,
// MediaFile and Search reconcilers, which take a record.EventRecorder and
// write core/v1. mgr.GetEventRecorder supplies it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SetupWithManager registers the RootFolder controller.
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
		Named("rootfolder").
		For(&catalogv1alpha1.RootFolder{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
