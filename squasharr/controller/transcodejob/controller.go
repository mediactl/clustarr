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

package transcodejob

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
	"github.com/mediactl/clustarr/squasharr/task"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Field-index names. Prefixed with the package so they cannot collide with
// catalogarr's own ".spec.mediaFileRef" index on TranscodeJob when
// `clustarr all` runs every service in one process.
const (
	indexMediaFileRef = "squasharr.transcodejob.spec.mediaFileRef"
	indexProfileRef   = "squasharr.transcodejob.spec.profileRef"
)

// Requeue intervals. Both are safety nets, not the primary wake path: the
// watches in SetupWithManager and the results consumer's admission wake are.
const (
	// requeueWaiting re-checks a Pending job whose MediaFile probe or
	// profile hash has not landed yet.
	requeueWaiting = 30 * time.Second

	// requeueQueued re-runs admission for a Planned, Queued or Running job,
	// so a job can never starve if the event that freed its slot was missed.
	requeueQueued = time.Minute
)

// admissionRequest is the request the results consumer enqueues when a job
// leaves Queued or Running: a slot is free, or a requeued job wants one.
// Reconcile runs one admission pass for it, inside the controller's single
// worker, so admission stays serialised (MaxConcurrentReconciles 1). A
// TranscodeJob is namespaced, so no real object has an empty namespace.
var admissionRequest = types.NamespacedName{Name: "(admission)"}

// deadLetteredTaskPrefix is the subject prefix of a dead-lettered task in
// the DLQ projector's annotation value ("<original-subject>@<RFC3339>"). A
// dead-lettered history event (clustarr.evt.transcode.job.*) about the same
// job folds into the DeadLettered condition only: it is not the job's work
// that failed.
const deadLetteredTaskPrefix = "clustarr.work.transcode.task."

// Condition reasons local to TranscodeJob.
const (
	ReasonPlanned         = "Planned"
	ReasonSkipped         = "Skipped"
	ReasonRejected        = "Rejected"
	ReasonContainerChange = "ContainerChange" // Planned=True's reason when the output changes container (R-11)
	ReasonInvalidOutput   = "InvalidOutput"
	ReasonWaiting         = "Waiting"
	ReasonSourceChanged   = "SourceChanged"
	ReasonPlanError       = "PlanError"
	ReasonJobSucceeded    = "JobSucceeded"
	ReasonJobFailed       = "JobFailed"
	ReasonWorkerVerified  = "WorkerVerified"
)

// Reconciler drives a TranscodeJob Pending -> Planned -> Queued -> Running ->
// Succeeded|Failed, or straight to Skipped, and runs the slot scheduler. It
// plans, admits and dispatches; the worker status events its results
// consumer (results.go) reads move a dispatched job on. Field manager:
// k8s.ManagerSquasharr, all of TranscodeJob.status, through one
// compare-and-swap write (write.go). See doc.go.
type Reconciler struct {
	// Client reads TranscodeProfiles and MediaFiles (cached under a
	// manager) and writes status.
	Client client.Client

	// Reader reads every TranscodeJob this controller writes and the
	// TranscodeJobs admission counts. Under a manager it must be
	// mgr.GetAPIReader(): a status write is conditional on the
	// resourceVersion it was read at, so a cached read would conflict on
	// every write the cache has not caught up with, and the slot count has
	// to see a job dispatched a moment ago (ADR-0005). Nil means Client.
	Reader client.Reader

	// Slots is the per-hardware budget, squasharr.Options.Slots.
	Slots map[string]int32

	// Pool is the deployment-level half of every pool Job the admission
	// pass renders (pools.go).
	Pool pool.Config

	// Recorder emits a Kubernetes Event on the TranscodeJob for each
	// lifecycle edge (events.go). Nil records none.
	Recorder k8sevents.EventRecorder

	// Bus publishes each dispatched job's task and the
	// clustarr.evt.transcode.job.<action> history (§5), and the results
	// consumer subscribes squasharr-transcode-results on it.
	Bus events.Bus

	// Leases is clustarr-transcode-leases, where withdrawal writes its
	// cancel markers (Task 12).
	Leases events.KV

	// Admin purges withdrawn and orphaned task subjects and is the sweep's
	// source of truth for what is stored (withdraw.go). Nil disables both:
	// a Reconciler built without it (a test that does not exercise
	// withdrawal) still runs, since withdraw and sweep are only ever called
	// from paths a bus-less test does not reach, except sweep itself, which
	// no-ops when Admin is nil.
	Admin events.StreamAdmin

	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// wake carries the results consumer's admission wake to the
	// controller (SetupWithManager); nil outside a manager.
	wake chan event.GenericEvent

	// recreate names the pool Jobs the apiserver refused a gang minCount
	// on (created before WorkloadWithJob was enabled): each drains and is
	// recreated. poolBackoff is each Failed pool's recreation backoff.
	// nextSweep is when admit's backstop sweep (withdraw.go) is next due.
	// Only admission touches any of these, and MaxConcurrentReconciles 1
	// serialises it, so admit initialises the maps lazily.
	recreate    map[string]bool
	poolBackoff map[string]poolBackoff
	nextSweep   time.Time
}

func (r *Reconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

func terminal(p transcodev1alpha1.TranscodeJobPhase) bool {
	switch p {
	case transcodev1alpha1.TranscodeJobPhaseSucceeded,
		transcodev1alpha1.TranscodeJobPhaseFailed,
		transcodev1alpha1.TranscodeJobPhaseSkipped:
		return true
	}
	return false
}

// Reconcile advances one TranscodeJob, then runs one admission pass.
//
// The job is read fresh, through Reader (ruling R2), and every status write
// is ONE compare-and-swap apply (patchCAS) of a status seeded from that read
// (desired := tj.Status.DeepCopy()), mutated by whichever phase steps ran,
// and rendered once at the end -- on the error path too. So a transient
// failure half way through still declares every field this manager owns,
// carrying forward what was there, rather than applying a partial status
// that releases the plan, jobRef and timestamps of a healthy job. And a
// result event that landed since the read makes the write conflict instead
// of being rolled back: the Conflict is returned, and the retry reads again.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.Reconcile")
	defer span.End()

	if req.NamespacedName == admissionRequest {
		if err := r.admit(ctx); err != nil {
			tracing.RecordError(span, err)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	var tj transcodev1alpha1.TranscodeJob
	if err := r.reader().Get(ctx, req.NamespacedName, &tj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&tj) {
		return r.reconcileDelete(ctx, &tj)
	}
	if terminal(tj.Status.Phase) {
		// Nothing about the transcode changes once the phase is terminal --
		// except the DLQ projector's annotation, which lands mostly on
		// FINISHED jobs (their succeeded/failed/skipped events are what
		// reach the history consumer). write folds it and writes only if
		// that changed the conditions, from a seed of the live status, so
		// every other field is re-declared as it stands.
		//
		// The withdrawal finalizer is released here too, as a backstop for a
		// release afterWrite's own attempt failed: a terminal job never
		// needs it again, whatever reconcile brought this pass about.
		if _, err := k8s.RemoveFinalizer(ctx, r.Client, &tj, FinalizerTaskWithdrawal); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.write(ctx, &tj, tj.Status.DeepCopy())
	}

	desired := tj.Status.DeepCopy()
	desired.ObservedGeneration = tj.Generation
	if desired.Phase == "" {
		desired.Phase = transcodev1alpha1.TranscodeJobPhasePending
	}

	res, stepErr := r.advance(ctx, &tj, desired)
	if stepErr != nil {
		tracing.RecordError(span, stepErr)
		desired.Message = "reconcile error: " + stepErr.Error()
	}
	if err := r.write(ctx, &tj, desired); err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, errors.Join(stepErr, err)
	}
	if stepErr != nil {
		return ctrl.Result{}, stepErr
	}

	// Admission runs after this job's status has landed, on every
	// non-terminal pass -- including the one that just made this job
	// Planned, which dispatches it when a slot is free.
	if err := r.admit(ctx); err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	return res, nil
}

// write is Reconcile's status write: the dead-letter fold, nothing at all
// when that leaves the status as read, else patchCAS against the
// resourceVersion tj was read at, then afterWrite for the edges it crossed.
func (r *Reconciler) write(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, desired *transcodev1alpha1.TranscodeJobStatus) error {
	k8s.MarkDeadLettered(tj, &desired.Conditions)
	if equality.Semantic.DeepEqual(tj.Status, *desired) {
		return nil
	}
	before := *tj.Status.DeepCopy()
	if err := r.patchCAS(ctx, tj, desired); err != nil {
		return err
	}
	after := tj.DeepCopy()
	after.Status = *desired
	r.afterWrite(ctx, after, &before)
	return nil
}

// afterWrite runs once a status write has landed, whichever path made it:
// the history and Kubernetes Events for each edge before -> after crossed,
// and, on the write that finished a job that ran, the transcode metrics.
//
// It is called only after the write, never before: every write is
// conditional (patchCAS), and one that loses its race to the other path
// never happened, so announcing its edges first would publish a transition
// -- a "failed" the results consumer overtook with "succeeded" -- that the
// object never made. The cost is an event lost to a crash between the write
// and the publish; history is best effort, status is the record.
//
// before is the status the conditional write replaced, exactly: a second
// writer seeded from the same read would have conflicted. So the terminal
// edge, and the metrics observed on it (ruling R9), happen once per job.
func (r *Reconciler) afterWrite(ctx context.Context, after *transcodev1alpha1.TranscodeJob, before *transcodev1alpha1.TranscodeJobStatus) {
	edges := transitions(before, &after.Status)
	r.publishJobEvents(ctx, after, &after.Status, after.Status.Result, edges)
	r.recordEvents(after, edges)
	if !terminal(before.Phase) && ranToCompletion(&after.Status) {
		r.observeFinished(ctx, after, &after.Status)
	}
	if !terminal(before.Phase) && terminal(after.Status.Phase) {
		// A finished event, a block, or a plan-time skip: nothing dispatched
		// can still be running, so the withdrawal finalizer is released at
		// once rather than waiting for a delete to hit reconcileDelete's
		// protocol. Re-read fresh (releaseFinalizerIfTerminal): after's own
		// resourceVersion predates the status write that just landed (write
		// builds it from the pre-write object plus the desired status,
		// never re-Gets), so removing the finalizer from it directly would
		// conflict against the write it is meant to follow.
		if err := r.releaseFinalizerIfTerminal(ctx, client.ObjectKeyFromObject(after)); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodejob: could not release the withdrawal finalizer",
				"transcodeJob", client.ObjectKeyFromObject(after).String(), "error", err)
		}
	}
}

// wakeAdmission asks the controller for one admission pass, without
// blocking: a wake already pending covers this one.
func (r *Reconciler) wakeAdmission() {
	if r.wake == nil {
		return
	}
	select {
	case r.wake <- event.GenericEvent{Object: &transcodev1alpha1.TranscodeJob{}}:
	default:
	}
}

// advance runs as many phase steps as can complete in this pass, mutating
// st. It never writes status itself.
//
//   - Pending is planned (plan).
//   - Planned waits for admission, which dispatches it (admit, dispatch):
//     until nextAttemptAt for a requeued job, else requeueQueued.
//   - Queued and Running are moved on by worker status events (results.go);
//     the pass only checks that their task was not dead-lettered.
func (r *Reconciler) advance(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) (ctrl.Result, error) {
	if st.Phase == transcodev1alpha1.TranscodeJobPhasePending {
		res, err := r.plan(ctx, tj, st)
		if err != nil || st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned {
			return res, err
		}
	}
	switch st.Phase {
	case transcodev1alpha1.TranscodeJobPhasePlanned:
		if st.Plan == nil {
			// Only reachable for an object whose status was hand-edited;
			// plan again rather than guess a hardware class.
			st.Phase = transcodev1alpha1.TranscodeJobPhasePending
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if st.NextAttemptAt != nil {
			if wait := st.NextAttemptAt.Sub(r.now().Time); wait > 0 {
				return ctrl.Result{RequeueAfter: wait}, nil
			}
		}
		return ctrl.Result{RequeueAfter: requeueQueued}, nil
	case transcodev1alpha1.TranscodeJobPhaseQueued, transcodev1alpha1.TranscodeJobPhaseRunning:
		if tj.Spec.Suspend != nil && *tj.Spec.Suspend {
			// User pause (spec §8): withdraw the dispatched task, then go
			// back to Planned so admission holds it there (it already skips
			// a Planned job with spec.suspend=true) until the field flips
			// back, which re-dispatches it as a new attempt. withdraw needs
			// no TranscodeProfile (ruling R23), so a job whose profile was
			// deleted is still withdrawn cleanly.
			if err := r.withdraw(ctx, tj); err != nil {
				return ctrl.Result{}, fmt.Errorf("transcodejob: withdraw for spec.suspend: %w", err)
			}
			st.Phase, st.WorkerPod, st.Progress, st.NextAttemptAt = transcodev1alpha1.TranscodeJobPhasePlanned, "", nil, nil
			st.Message = "paused by spec.suspend"
			k8s.MarkFalse(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonSuspended, "%s", st.Message)
			return ctrl.Result{}, nil
		}
		if subject, ok := deadLetteredTask(tj); ok {
			// The queue gave up on the task (spec §13, §18.3): no worker will
			// report on it, so nothing else would ever move the job on.
			now := r.now().Time
			applyDecision(tj, st, task.StatusEvent{At: now}, Decision{
				Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: string(task.ReasonDeadLettered),
				Message: fmt.Sprintf("the task was dead-lettered after its last delivery (%s)", subject),
			}, now)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueQueued}, nil
	}
	return ctrl.Result{}, nil
}

// deadLetteredTask reports whether tj carries the DLQ projector's annotation
// for one of its own tasks, and that task's subject.
func deadLetteredTask(tj *transcodev1alpha1.TranscodeJob) (string, bool) {
	v, ok := tj.Annotations[k8s.AnnotationDeadLettered]
	if !ok {
		return "", false
	}
	subject := v
	if i := strings.LastIndex(v, "@"); i >= 0 {
		subject = v[:i]
	}
	if !strings.HasPrefix(subject, deadLetteredTaskPrefix) || !strings.HasSuffix(subject, "."+string(tj.UID)) {
		return "", false
	}
	return subject, true
}

// plan is Pending -> Planned, or -> Skipped/Failed for a decision not to
// transcode, planning from the MediaFile's stored probe (ruling R3).
func (r *Reconciler) plan(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.plan")
	defer span.End()

	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("transcodejob: get MediaFile %s: %w", tj.Spec.MediaFileRef, err)
		}
		r.wait(tj, st, "waiting for MediaFile %s", tj.Spec.MediaFileRef)
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}
	if mf.Status.MediaInfo == nil {
		r.wait(tj, st, "waiting for MediaFile %s to be probed", mf.Name)
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}
	if tj.Spec.SourceProbeHash != "" && mf.Status.ProbeHash != "" && mf.Status.ProbeHash != tj.Spec.SourceProbeHash {
		// The file changed after this job was created for it. The worker
		// would refuse it with exit 3 (R3); refusing here costs no pod.
		r.fail(tj, st, ReasonSourceChanged,
			"source probe hash %s no longer matches spec.sourceProbeHash %s; the file changed since this job was created",
			mf.Status.ProbeHash, tj.Spec.SourceProbeHash)
		return ctrl.Result{}, nil
	}

	profile, ok, err := r.profile(ctx, tj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		r.wait(tj, st, "waiting for TranscodeProfile %s", tj.Spec.ProfileRef)
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}
	if profile.Status.Hash == "" {
		// The hash is the TranscodeProfile controller's to compute (task
		// E-1); planning against a hash computed here could disagree with
		// the one catalogarr tags the output with.
		r.wait(tj, st, "waiting for TranscodeProfile %s to be hashed", profile.Name)
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}

	source := tj.Spec.SourcePath
	if source == "" {
		source = mf.Spec.Path
	}
	source = filepath.Clean(source) // the worker's input path, rendered into the argv
	// Where the output lands (gap-fix ruling R-11): the same function the
	// worker writes it with, so the plan's .part and argv name the path the
	// worker uses. A container change or a kept source (replaceSource=false)
	// is a new name beside the source; Phase E ruling R8's "container change
	// is Skipped" is gone.
	outPath, err := worker.OutputPath(tj.Spec, profile.Name, profile.Spec.Container, worker.ReplaceSource(profile.Spec.Policy))
	if err != nil {
		r.fail(tj, st, ReasonInvalidOutput, "cannot place the output: %v", err)
		return ctrl.Result{}, nil
	}
	info, err := mediaInfoFromFile(source, &mf, profile.Name+"@"+profile.Status.Hash)
	if err != nil {
		r.fail(tj, st, ReasonPlanError, "planning failed: %v", err)
		return ctrl.Result{}, nil
	}

	// worker.ProfileSpec, not a converter of this package's own: the plan
	// recorded here must be made from the same profile the worker executes,
	// with the same thread count and output path, so status.plan.argsHash is
	// the hash of the worker's argv.
	result, err := transcode.Plan(info, worker.ProfileSpec(profile.Spec, tj.Spec.Hardware), allEncoders(),
		transcode.PlanMeta{
			ProfileName: profile.Name, ProfileHash: profile.Status.Hash,
			Threads: pool.Threads(profile), OutputPath: outPath,
		})
	if err != nil {
		// Plan errors only on inputs that no retry fixes (no video stream,
		// an unknown hardware class the CRD enum should already reject).
		r.fail(tj, st, ReasonPlanError, "planning failed: %v", err)
		return ctrl.Result{}, nil
	}

	now := r.now()
	switch result.Decision {
	case transcode.DecisionReject:
		// Ruling R1: a deliberate decision not to transcode is Skipped, with
		// status.plan left unset because PlanMode has no reject value.
		st.Phase = transcodev1alpha1.TranscodeJobPhaseSkipped
		st.Message = "rejected: " + result.Reason
		st.FinishedAt = &now
		k8s.MarkFalse(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, ReasonRejected, "%s", result.Reason)
	case transcode.DecisionSkip:
		st.Phase = transcodev1alpha1.TranscodeJobPhaseSkipped
		st.Plan = statusPlan(result)
		st.Message = "skipped: " + result.Reason
		st.FinishedAt = &now
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, ReasonSkipped, "%s", result.Reason)
	default:
		st.Phase = transcodev1alpha1.TranscodeJobPhasePlanned
		st.Plan = statusPlan(result)
		st.Message = result.Reason
		reason, where := ReasonPlanned, ""
		if outPath != source {
			where = "; output " + outPath
			if src, want, changed := containerChange(source, profile.Spec.Container); changed {
				reason = ReasonContainerChange
				where = fmt.Sprintf("; .%s becomes %s at %s", src, want, outPath)
			}
		}
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, reason,
			"%s with %s%s", st.Plan.Mode, st.Plan.Encoder, where)
	}
	return ctrl.Result{}, nil
}

// admit is one pass of the slot scheduler: every Planned job that is not
// paused, deleting or waiting out its nextAttemptAt competes, under
// [Admit], for the slots the dispatched (Queued or Running) jobs leave free,
// and each one admitted is dispatched. A job whose pool is draining for a
// profile change, recovering from a failure, or held by a Job it does not
// own is held out of the competition (pools.go, holding). Then every pool is
// sized to the work dispatched to it (pools).
//
// TranscodeJobs are listed through Reader (uncached) so the count includes a
// job the previous pass dispatched even if the informer has not caught up;
// with MaxConcurrentReconciles pinned to 1 in SetupWithManager, and the
// results consumer's and the pool Job watch's wakes routed through the same
// queue, that makes over-admission impossible from inside one process.
func (r *Reconciler) admit(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.admit")
	defer span.End()
	log := logging.FromContext(ctx)
	if r.recreate == nil {
		r.recreate, r.poolBackoff = map[string]bool{}, map[string]poolBackoff{}
	}

	var tjs transcodev1alpha1.TranscodeJobList
	if err := r.reader().List(ctx, &tjs); err != nil {
		return fmt.Errorf("transcodejob: list TranscodeJobs: %w", err)
	}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles); err != nil {
		return fmt.Errorf("transcodejob: list TranscodeProfiles: %w", err)
	}
	byName := make(map[string]*transcodev1alpha1.TranscodeProfile, len(profiles.Items))
	for i := range profiles.Items {
		byName[profiles.Items[i].Name] = &profiles.Items[i]
	}
	stored, err := r.poolJobs(ctx)
	if err != nil {
		return err
	}
	held := r.holding(stored, byName)

	now := r.now().Time
	var (
		queued, running []Slot
		errs            []error
	)
	// The backstop sweep (withdraw.go), due at most once per sweepInterval:
	// every TranscodeJob that exists, of any phase, protects its own task
	// subject from it.
	errs = append(errs, r.sweep(ctx, tjs.Items))
	candidates := map[string]*transcodev1alpha1.TranscodeJob{}
	for i := range tjs.Items {
		tj := &tjs.Items[i]
		key := tj.Namespace + "/" + tj.Name
		switch {
		case dispatched(tj.Status.Phase):
			running = append(running, Slot{Key: key, Hardware: string(tj.Status.Hardware), Profile: tj.Spec.ProfileRef})
		case tj.Status.Phase == transcodev1alpha1.TranscodeJobPhasePlanned:
			if k8s.IsDeleting(tj) || (tj.Spec.Suspend != nil && *tj.Spec.Suspend) ||
				(tj.Status.NextAttemptAt != nil && tj.Status.NextAttemptAt.After(now)) {
				continue
			}
			tp, ok := byName[tj.Spec.ProfileRef]
			if !ok {
				continue
			}
			class := r.classFor(tj, tp)
			if msg, ok := held[poolKeyFor(tp, class)]; ok {
				errs = append(errs, r.setHeldMessage(ctx, tj, msg))
				continue
			}
			slot := Slot{
				Key: key, Hardware: string(class), Profile: tj.Spec.ProfileRef,
				Priority: tj.Spec.Priority, Created: tj.CreationTimestamp.Time,
			}
			if slot.Priority == 0 {
				slot.Priority = tp.Spec.Priority
			}
			queued = append(queued, slot)
			candidates[key] = tj
		}
	}

	admitted := Admit(queued, running, Budget{Slots: r.Slots, ProfileLimits: profileLimits(profiles.Items)})
	setActive(r.Slots, running, admitted)
	for _, s := range admitted {
		delete(candidates, s.Key)
		ns, name, _ := strings.Cut(s.Key, "/")
		if err := r.dispatch(ctx, types.NamespacedName{Namespace: ns, Name: name}, transcodev1alpha1.Hardware(s.Hardware)); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Info("dispatched transcode", "transcodeJob", s.Key, "hardware", s.Hardware, "priority", s.Priority)
	}
	for _, s := range queued {
		// A job held by an earlier pass that is free now but found no slot
		// says so, rather than claim a drain that is over.
		if tj, ok := candidates[s.Key]; ok && strings.HasPrefix(tj.Status.Message, heldPrefix) {
			errs = append(errs, r.setHeldMessage(ctx, tj, fmt.Sprintf("waiting for a free %s slot", s.Hardware)))
		}
	}

	if len(admitted) > 0 {
		// Count what the dispatches recorded: status.hardware is the class
		// each task went to.
		tjs = transcodev1alpha1.TranscodeJobList{}
		if err := r.reader().List(ctx, &tjs); err != nil {
			return errors.Join(append(errs, fmt.Errorf("transcodejob: list TranscodeJobs: %w", err))...)
		}
	}
	errs = append(errs, r.pools(ctx, stored, byName, dispatchedPerPool(tjs.Items, byName)))
	return errors.Join(errs...)
}

// profileLimits is Budget.ProfileLimits from each profile's
// spec.maxConcurrent. A zero (or absent) maxConcurrent is left out, which
// Admit reads as "no per-profile cap".
func profileLimits(profiles []transcodev1alpha1.TranscodeProfile) map[string]int32 {
	out := map[string]int32{}
	for i := range profiles {
		if n := profiles[i].Spec.MaxConcurrent; n > 0 {
			out[profiles[i].Name] = n
		}
	}
	return out
}

// profile returns the job's TranscodeProfile, reporting false if it does not
// exist (yet).
func (r *Reconciler) profile(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) (*transcodev1alpha1.TranscodeProfile, bool, error) {
	var p transcodev1alpha1.TranscodeProfile
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("transcodejob: get TranscodeProfile %s: %w", tj.Spec.ProfileRef, err)
	}
	return &p, true, nil
}

// wait keeps the job Pending with Planned=False and a reason.
func (r *Reconciler) wait(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus, format string, args ...any) {
	st.Phase = transcodev1alpha1.TranscodeJobPhasePending
	st.Message = fmt.Sprintf(format, args...)
	k8s.MarkFalse(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, ReasonWaiting, "%s", st.Message)
}

// fail is a terminal Failed with Failed=True.
func (r *Reconciler) fail(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus, reason, format string, args ...any) {
	now := r.now()
	st.Phase = transcodev1alpha1.TranscodeJobPhaseFailed
	st.Message = fmt.Sprintf(format, args...)
	st.FinishedAt = &now
	k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionFailed, reason, "%s", st.Message)
}

// profileSignal wakes Pending jobs when their profile's hash first lands
// (or changes).
func profileSignal(o client.Object) string {
	if p, ok := o.(*transcodev1alpha1.TranscodeProfile); ok {
		return p.Status.Hash
	}
	return ""
}

// mediaFileSignal wakes Pending jobs when their file's probe lands.
func mediaFileSignal(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s|%t", mf.Status.ProbeHash, mf.Status.MediaInfo != nil)
}

// transcodeJobPredicate is the controller's own filter on TranscodeJob:
// create and spec change (GenerationChanged), plus the DLQ projector's
// annotation appearing, changing or being removed, which touches neither
// generation nor status (DeadLetteredAnnotationChanged). NOT its own status:
// the results consumer writes progress every ~10s per running job, and what
// the reconciler needs from those writes -- a freed slot -- it gets from the
// consumer's admission wake instead.
func transcodeJobPredicate() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.DeadLetteredAnnotationChanged())
}

// SetupWithManager registers the TranscodeJob controller.
//
// What wakes it, and why each is needed:
//   - TranscodeJob create and spec change (spec.suspend, spec.priority), and
//     the DLQ projector's annotation ([transcodeJobPredicate]).
//   - A TranscodeProfile's spec or status.hash ([profileSignal]), mapped to
//     its non-terminal jobs, for a job Pending on the hash.
//   - A MediaFile's probe ([mediaFileSignal]), mapped to its non-terminal
//     jobs, for a job Pending on the probe.
//   - The results consumer's admission wake (results.go), when a job leaves
//     Queued or Running: one admission pass, [admissionRequest].
//   - A pool Job's creation, deletion, or change to its suspend, pods,
//     startTime, failure or template hash ([poolJobPredicate]): one
//     admission pass, which is what acts on a pool -- a stopped pool
//     reshaped and its held work dispatched, a Failed or deleted pool
//     recreated. One pass, not one per job of the profile: every pass
//     already considers every job.
//
// Worker status events are not a watch: they arrive through the results
// consumer, a separate runnable (ResultsConsumer) the caller adds to the
// manager.
//
// MaxConcurrentReconciles is pinned to 1: admission counts slots then
// dispatches, and two concurrent passes could both see the same free slot.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(context.Background(), &transcodev1alpha1.TranscodeJob{}, indexMediaFileRef,
		func(o client.Object) []string {
			if tj, ok := o.(*transcodev1alpha1.TranscodeJob); ok && tj.Spec.MediaFileRef != "" {
				return []string{tj.Spec.MediaFileRef}
			}
			return nil
		}); err != nil {
		return err
	}
	if err := idx.IndexField(context.Background(), &transcodev1alpha1.TranscodeJob{}, indexProfileRef,
		func(o client.Object) []string {
			if tj, ok := o.(*transcodev1alpha1.TranscodeJob); ok && tj.Spec.ProfileRef != "" {
				return []string{tj.Spec.ProfileRef}
			}
			return nil
		}); err != nil {
		return err
	}

	r.wake = make(chan event.GenericEvent, 1)
	admission := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: admissionRequest}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("transcodejob").
		For(&transcodev1alpha1.TranscodeJob{}, builder.WithPredicates(transcodeJobPredicate())).
		Watches(&transcodev1alpha1.TranscodeProfile{},
			handler.EnqueueRequestsFromMapFunc(r.mapIndexed(indexProfileRef, false)),
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(profileSignal)))).
		Watches(&catalogv1alpha1.MediaFile{},
			handler.EnqueueRequestsFromMapFunc(r.mapIndexed(indexMediaFileRef, true)),
			builder.WithPredicates(k8s.StatusFieldChanged(mediaFileSignal))).
		Watches(&batchv1.Job{}, admission, builder.WithPredicates(poolJobPredicate())).
		WatchesRawSource(source.Channel(r.wake, admission)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// mapIndexed maps a profile or MediaFile to the non-terminal TranscodeJobs
// that reference it through index. namespaced restricts the lookup to the
// object's own namespace (MediaFile); a cluster-scoped profile is referenced
// from every namespace.
func (r *Reconciler) mapIndexed(index string, namespaced bool) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		opts := []client.ListOption{client.MatchingFields{index: o.GetName()}}
		if namespaced {
			opts = append(opts, client.InNamespace(o.GetNamespace()))
		}
		var list transcodev1alpha1.TranscodeJobList
		if err := r.Client.List(ctx, &list, opts...); err != nil {
			logging.FromContext(ctx).Error("transcodejob: map to TranscodeJobs", "index", index, "err", err)
			return nil
		}
		var out []reconcile.Request
		for _, tj := range list.Items {
			if terminal(tj.Status.Phase) {
				continue
			}
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: tj.Namespace, Name: tj.Name}})
		}
		return out
	}
}
