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

package rootfolderschedule

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
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
)

const (
	// LabelRootFolder marks a LibraryScan as this schedule's work, and is
	// how the controller finds the scans it has already created.
	LabelRootFolder = "catalog.clustarr.io/root-folder"

	// AnnotationLastTick records, on the RootFolder itself, the scheduled
	// time this controller last fired for. See the package doc for why the
	// record cannot be "the newest LibraryScan's creationTimestamp".
	AnnotationLastTick = "catalog.clustarr.io/last-scan-tick"

	// idleRecheck is how often a RootFolder with no schedule at all is
	// looked at again. It is not zero so that a schedule added later is
	// noticed even if the spec change somehow misses the watch.
	idleRecheck = time.Hour

	// maxRequeue caps how far ahead the controller will sleep. A yearly
	// schedule would otherwise ask for a twelve-month RequeueAfter, and a
	// timer that long is a timer nobody can reason about.
	maxRequeue = 12 * time.Hour
)

// Event reasons this controller emits.
const (
	ReasonInvalidScanSchedule = "InvalidScanSchedule"
	ReasonScanScheduled       = "ScanScheduled"
)

// Reconciler creates one LibraryScan per RootFolder.spec.scanSchedule tick.
//
// It never writes RootFolder.status -- that belongs to catalogarr's RootFolder
// reconciler -- and writes exactly one thing on the RootFolder itself, the
// [AnnotationLastTick] annotation, under k8s.ManagerImportarr.
type Reconciler struct {
	// Client reads the RootFolder and its scans, and applies both the new
	// LibraryScan and the tick annotation.
	Client client.Client

	// Recorder surfaces an unusable cron expression to the user, which is
	// the one failure a status condition cannot carry here.
	Recorder record.EventRecorder

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "rootfolderschedule.Reconcile")
	defer span.End()
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With("rootFolder", req.NamespacedName.String()))

	var rf catalogv1alpha1.RootFolder
	if err := r.Client.Get(ctx, req.NamespacedName, &rf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&rf) {
		return ctrl.Result{}, nil
	}
	if rf.Spec.ScanSchedule == "" {
		return ctrl.Result{RequeueAfter: idleRecheck}, nil
	}

	sched, err := ParseSchedule(rf.Spec.ScanSchedule)
	if err != nil {
		// An unparseable schedule cannot be fixed by retrying, and this
		// controller may not write RootFolder.status to say so, so the
		// Event is the whole user-facing signal.
		if r.Recorder != nil {
			r.Recorder.Eventf(&rf, corev1.EventTypeWarning, ReasonInvalidScanSchedule,
				"cron expression %q is invalid: %v", rf.Spec.ScanSchedule, err)
		}
		return ctrl.Result{}, reconcile.TerminalError(
			fmt.Errorf("rootfolderschedule: %s: %w", req.NamespacedName, err))
	}

	now := r.now()
	last, adopted := lastTick(&rf)
	if !adopted {
		// Never seen before: scan once on adoption, then let the schedule
		// take over. Everything after this is driven by the annotation.
		return r.fire(ctx, &rf, now.Truncate(time.Minute))
	}

	next := sched.Next(last)
	if next.IsZero() {
		if r.Recorder != nil {
			r.Recorder.Eventf(&rf, corev1.EventTypeWarning, ReasonInvalidScanSchedule,
				"cron expression %q will never fire again", rf.Spec.ScanSchedule)
		}
		return ctrl.Result{}, reconcile.TerminalError(fmt.Errorf(
			"rootfolderschedule: %s: cron expression %q will never fire again",
			req.NamespacedName, rf.Spec.ScanSchedule))
	}
	if next.After(now) {
		return ctrl.Result{RequeueAfter: requeueFor(next.Sub(now))}, nil
	}

	// The tick is due. Ticks are not backfilled: if several are past, the
	// most recent one is fired and the rest are dropped, so a controller
	// that was down for a week does not walk the library seven times.
	tick := next
	for {
		following := sched.Next(tick)
		if following.IsZero() || following.After(now) {
			break
		}
		tick = following
	}
	return r.fire(ctx, &rf, tick)
}

// fire creates the LibraryScan for one tick and records the tick on the
// RootFolder. The scan's name is derived from the tick, so a retry between
// the two writes re-applies the same object rather than creating a second.
func (r *Reconciler) fire(
	ctx context.Context,
	rf *catalogv1alpha1.RootFolder,
	tick time.Time,
) (ctrl.Result, error) {
	name := k8s.ChildName(rf.Name, "scan", tick.UTC().Format(time.RFC3339))

	scan := catalogac.LibraryScan(name, rf.Namespace).
		WithLabels(map[string]string{LabelRootFolder: rf.Name}).
		WithSpec(catalogac.LibraryScanSpec().
			WithRootFolderRef(rf.Name).
			WithMode(catalogv1alpha1.ScanModeIncremental))
	// No owner reference: a LibraryScan outlives nothing and deletes itself
	// on its own TTL. Garbage-collecting scans when the RootFolder goes away
	// would also destroy the record of what the last walk found.
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerImportarr, scan); err != nil {
		return ctrl.Result{}, err
	}

	stamp := catalogac.RootFolder(rf.Name, rf.Namespace).
		WithAnnotations(map[string]string{AnnotationLastTick: tick.UTC().Format(time.RFC3339)})
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerImportarr, stamp); err != nil {
		// The scan exists; only the bookkeeping failed. Retrying re-applies
		// the same scan name, so this is safe to repeat.
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(rf, corev1.EventTypeNormal, ReasonScanScheduled,
			"created LibraryScan %s for the %s tick", name, tick.UTC().Format(time.RFC3339))
	}
	logging.FromContext(ctx).Info("library scan scheduled", "scan", name, "tick", tick)
	return ctrl.Result{RequeueAfter: requeueFor(time.Minute)}, nil
}

// lastTick reads [AnnotationLastTick]. The second return is false when the
// RootFolder has never been scheduled before, or when the annotation is
// unreadable -- in which case re-adopting is safer than firing for a tick
// derived from garbage.
func lastTick(rf *catalogv1alpha1.RootFolder) (time.Time, bool) {
	raw, ok := rf.Annotations[AnnotationLastTick]
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// requeueFor clamps a sleep to something a human can reason about and keeps
// it strictly positive, since a zero RequeueAfter means "do not requeue".
func requeueFor(d time.Duration) time.Duration {
	switch {
	case d > maxRequeue:
		return maxRequeue
	case d < time.Second:
		return time.Second
	default:
		return d
	}
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// SetupWithManager registers the schedule controller.
//
// There is no Owns or Watches on LibraryScan: the schedule is entirely
// self-timed through RequeueAfter, and a scan carries no owner reference back
// to the RootFolder that triggered it. The predicate is GenerationChanged so
// that this controller's own annotation write -- which does not bump the
// generation -- cannot loop it, and neither can catalogarr's RootFolder
// status writes.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("rootfolderschedule").
		For(&catalogv1alpha1.RootFolder{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// A signature drift is then a compile error rather than a runtime surprise.
var _ reconcile.Reconciler = (*Reconciler)(nil)
