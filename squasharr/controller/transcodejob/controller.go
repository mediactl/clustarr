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
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
	squasharrstatus "github.com/mediactl/clustarr/squasharr/status"
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
// watches in SetupWithManager are.
const (
	// requeueWaiting re-checks a Pending job whose MediaFile probe or
	// profile hash has not landed yet.
	requeueWaiting = 30 * time.Second

	// requeueQueued re-runs admission for a Queued job, so a job can never
	// starve if the event that freed its slot was missed.
	requeueQueued = time.Minute
)

// Condition reasons local to TranscodeJob.
const (
	ReasonPlanned         = "Planned"
	ReasonSkipped         = "Skipped"
	ReasonRejected        = "Rejected"
	ReasonContainerChange = "ContainerChange"
	ReasonWaiting         = "Waiting"
	ReasonSourceChanged   = "SourceChanged"
	ReasonPlanError       = "PlanError"
	ReasonJobCreated      = "JobCreated"
	ReasonJobDeleted      = "JobDeleted"
	ReasonJobSucceeded    = "JobSucceeded"
	ReasonJobFailed       = "JobFailed"
	ReasonWorkerVerified  = "WorkerVerified"
)

// Reconciler drives a TranscodeJob Pending -> Planned -> Queued -> Running ->
// Succeeded|Failed, or straight to Skipped, and runs the slot scheduler.
// Field manager: k8s.ManagerSquasharr, and only the controller half of
// TranscodeJob.status (squasharr/status.ControllerFields). See doc.go.
type Reconciler struct {
	// Client reads TranscodeJobs, TranscodeProfiles and MediaFiles (cached
	// under a manager) and writes Jobs and status.
	Client client.Client

	// Reader reads batch Jobs for admission and phase mirroring. Under a
	// manager this must be mgr.GetAPIReader(): the slot count has to see a
	// Job this controller unsuspended a moment ago, and a cache lagging by
	// one event would admit past the budget -- exactly the failure ADR-0005
	// warns about. Nil means Client.
	Reader client.Reader

	// Slots is the per-hardware budget, squasharr.Options.Slots.
	Slots map[string]int32

	// ProfileLimits optionally caps concurrent transcodes per profile; see
	// Budget.ProfileLimits.
	ProfileLimits map[string]int32

	// Job is the deployment-level half of every Job this creates.
	Job JobConfig

	// Now is the clock. Nil means time.Now.
	Now func() time.Time
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
// Every status write is ONE apply of a status seeded from the live object
// (desired := tj.Status.DeepCopy()), mutated by whichever phase steps ran,
// and rendered once at the end -- on the error path too. So a transient
// failure half way through (the apiserver refusing a Job create, a Get that
// times out) still declares every field this manager owns, carrying forward
// what was there, rather than applying a partial status that releases the
// plan, jobRef and timestamps of a healthy job. That is the single most
// repeated bug in this project; the shape makes it structural here rather
// than something each early return has to remember.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.Reconcile")
	defer span.End()

	var tj transcodev1alpha1.TranscodeJob
	if err := r.Client.Get(ctx, req.NamespacedName, &tj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&tj) || terminal(tj.Status.Phase) {
		// A finished job's Job is TTL-collected by Kubernetes; nothing here
		// changes once the phase is terminal.
		return ctrl.Result{}, nil
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

	// Ruling R9: a job that ran and just finished is observed into the
	// transcode metrics exactly once. The object above came from the cache,
	// which can lag this controller's own terminal write by an event; a
	// fresh read decides whether this pass is the transition, and supplies
	// the worker's status.result as it is now.
	var finished *transcodev1alpha1.TranscodeJob
	if stepErr == nil && ranToCompletion(desired) {
		var fresh transcodev1alpha1.TranscodeJob
		if err := r.reader().Get(ctx, req.NamespacedName, &fresh); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if terminal(fresh.Status.Phase) {
			return ctrl.Result{}, nil // an earlier pass already made (and counted) the transition
		}
		finished = &fresh
	}

	if err := r.apply(ctx, &tj, desired); err != nil {
		return ctrl.Result{}, errors.Join(stepErr, err)
	}
	if stepErr != nil {
		return ctrl.Result{}, stepErr
	}
	if finished != nil {
		r.observeFinished(ctx, finished, desired)
	}

	// Admission runs after this job's status has landed, on every
	// non-terminal pass -- including the one that just moved this job to
	// Succeeded or Failed, which is the pass that freed a slot.
	if err := r.admit(ctx); err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	return res, nil
}

// advance runs as many phase steps as can complete in this pass, mutating
// st. It never writes status itself.
func (r *Reconciler) advance(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) (ctrl.Result, error) {
	if st.Phase == transcodev1alpha1.TranscodeJobPhasePending {
		res, err := r.plan(ctx, tj, st)
		if err != nil || st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned {
			return res, err
		}
	}
	if st.Phase == transcodev1alpha1.TranscodeJobPhasePlanned || st.JobRef == nil {
		res, err := r.ensureJob(ctx, tj, st)
		if err != nil || st.Phase != transcodev1alpha1.TranscodeJobPhaseQueued {
			return res, err
		}
	}
	return r.observe(ctx, tj, st)
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
	if src, want, ok := containerChange(source, profile.Spec.Container); ok {
		// Ruling R8: the worker writes its output over the source PATH
		// (R5), so a container change would put mkv data behind an .mp4
		// name, or the reverse. Until a library path migration exists this
		// is a decision not to transcode, like a reject: Skipped, no plan.
		now := r.now()
		msg := fmt.Sprintf("source is .%s but profile %s writes %s: a container change requires a library path migration, which is not yet supported",
			src, profile.Name, want)
		st.Phase = transcodev1alpha1.TranscodeJobPhaseSkipped
		st.Message = msg
		st.FinishedAt = &now
		k8s.MarkFalse(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, ReasonContainerChange, "%s", msg)
		return ctrl.Result{}, nil
	}

	// worker.ProfileSpec, not a converter of this package's own: the plan
	// recorded here must be made from the same profile the worker executes.
	result, err := transcode.Plan(mediaInfoFromFile(source, &mf), worker.ProfileSpec(profile.Spec, tj.Spec.Hardware),
		allEncoders(), transcode.PlanMeta{ProfileName: profile.Name, ProfileHash: profile.Status.Hash})
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
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned, ReasonPlanned,
			"%s with %s", st.Plan.Mode, st.Plan.Encoder)
	}
	return ctrl.Result{}, nil
}

// ensureJob is Planned -> Queued: create the suspended Job, create-if-absent
// by deterministic name. It also recreates a Job that vanished while the
// TranscodeJob was still Queued (deleted by hand before it ever ran).
func (r *Reconciler) ensureJob(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.ensureJob")
	defer span.End()

	if st.Plan == nil {
		// Only reachable for an object whose status was hand-edited; plan
		// again rather than guess a hardware class.
		st.Phase = transcodev1alpha1.TranscodeJobPhasePending
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	profile, ok, err := r.profile(ctx, tj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		st.Message = fmt.Sprintf("waiting for TranscodeProfile %s", tj.Spec.ProfileRef)
		return ctrl.Result{RequeueAfter: requeueWaiting}, nil
	}

	hardware := hardwareForEncoder(st.Plan.Encoder)
	job := buildJob(tj, profile, hardware, r.Job)
	if err := k8s.SetControllerReference(tj, job, r.Client.Scheme()); err != nil {
		return ctrl.Result{}, fmt.Errorf("transcodejob: owner reference: %w", err)
	}
	if err := r.Client.Create(ctx, job, client.FieldOwner(k8s.ManagerSquasharr.String())); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("transcodejob: create Job %s: %w", job.Name, err)
		}
		var existing batchv1.Job
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(job), &existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("transcodejob: get existing Job %s: %w", job.Name, err)
		}
		if !metav1.IsControlledBy(&existing, tj) {
			return ctrl.Result{}, reconcile.TerminalError(
				fmt.Errorf("transcodejob: Job %s/%s exists and is not controlled by this TranscodeJob", job.Namespace, job.Name))
		}
	}

	st.Phase = transcodev1alpha1.TranscodeJobPhaseQueued
	st.JobRef = ptr.To(job.Name)
	st.Message = fmt.Sprintf("queued for a %s slot", hardware)
	k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonJobCreated,
		"Job %s created suspended", job.Name)
	return ctrl.Result{RequeueAfter: requeueQueued}, nil
}

// observe mirrors the Job into phase: Queued while suspended, Running once
// unsuspended, Succeeded/Failed from the Job's terminal condition. It also
// honours spec.suspend, the user's pause.
func (r *Reconciler) observe(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.observe")
	defer span.End()

	var job batchv1.Job
	if err := r.reader().Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: *st.JobRef}, &job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("transcodejob: get Job %s: %w", *st.JobRef, err)
		}
		if st.Phase == transcodev1alpha1.TranscodeJobPhaseQueued {
			// Never started: recreating it loses nothing.
			return r.ensureJob(ctx, tj, st)
		}
		r.fail(tj, st, ReasonJobDeleted, "Job %s was deleted while the transcode was %s", *st.JobRef, st.Phase)
		return ctrl.Result{}, nil
	}

	if n := job.Status.Failed + job.Status.Succeeded + job.Status.Active; n > st.Attempts {
		st.Attempts = n
	}

	if done, ok, cond := jobFinished(&job); done {
		finished := r.now()
		if job.Status.CompletionTime != nil {
			finished = *job.Status.CompletionTime
		} else if !cond.LastTransitionTime.IsZero() {
			finished = cond.LastTransitionTime
		}
		st.FinishedAt = &finished
		if ok {
			st.Phase = transcodev1alpha1.TranscodeJobPhaseSucceeded
			st.Message = "transcode succeeded"
			// The worker exits 0 only after Verifier.Verify passed and the
			// swap completed, so a Complete Job is a verified one.
			k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionVerified, ReasonWorkerVerified,
				"the worker verified the output before exiting 0")
			k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionSucceeded, ReasonJobSucceeded,
				"Job %s completed", job.Name)
			return ctrl.Result{}, nil
		}
		reason := cond.Reason
		if reason == "" {
			reason = ReasonJobFailed
		}
		st.Phase = transcodev1alpha1.TranscodeJobPhaseFailed
		st.Message = fmt.Sprintf("Job %s failed: %s", job.Name, strings.TrimSpace(reason+": "+cond.Message))
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionFailed, reason, "Job %s failed: %s", job.Name, cond.Message)
		return ctrl.Result{}, nil
	}

	userSuspended := tj.Spec.Suspend != nil && *tj.Spec.Suspend
	if userSuspended && !jobSuspended(&job) {
		if err := r.setSuspend(ctx, &job, true); err != nil {
			return ctrl.Result{}, err
		}
	}
	if userSuspended || jobSuspended(&job) {
		st.Phase = transcodev1alpha1.TranscodeJobPhaseQueued
		st.Message = fmt.Sprintf("queued for a %s slot", job.Labels[LabelHardware])
		if userSuspended {
			st.Message = "paused by spec.suspend"
		}
		return ctrl.Result{RequeueAfter: requeueQueued}, nil
	}

	st.Phase = transcodev1alpha1.TranscodeJobPhaseRunning
	st.Message = fmt.Sprintf("running on a %s slot", job.Labels[LabelHardware])
	if st.StartedAt == nil {
		started := r.now()
		if job.Status.StartTime != nil {
			started = *job.Status.StartTime
		}
		st.StartedAt = &started
	}
	return ctrl.Result{}, nil
}

// admit is one pass of the slot scheduler: every suspended, unfinished
// squasharr Job whose TranscodeJob is not paused competes, under [Admit],
// for the slots the unsuspended, unfinished ones leave free.
//
// Jobs are read through Reader (uncached) so the running count includes a
// Job unsuspended by the previous pass even if the informer has not caught
// up; with MaxConcurrentReconciles pinned to 1 in SetupWithManager, that
// makes over-admission impossible from inside one process.
func (r *Reconciler) admit(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.admit")
	defer span.End()
	log := logging.FromContext(ctx)

	var jobs batchv1.JobList
	if err := r.reader().List(ctx, &jobs, client.MatchingLabels{LabelManagedBy: ManagedByValue}); err != nil {
		return fmt.Errorf("transcodejob: list Jobs: %w", err)
	}
	var tjs transcodev1alpha1.TranscodeJobList
	if err := r.Client.List(ctx, &tjs); err != nil {
		return fmt.Errorf("transcodejob: list TranscodeJobs: %w", err)
	}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles); err != nil {
		return fmt.Errorf("transcodejob: list TranscodeProfiles: %w", err)
	}

	owners := make(map[types.UID]*transcodev1alpha1.TranscodeJob, len(tjs.Items))
	for i := range tjs.Items {
		owners[tjs.Items[i].UID] = &tjs.Items[i]
	}
	profilePriority := make(map[string]int32, len(profiles.Items))
	for _, p := range profiles.Items {
		profilePriority[p.Name] = p.Spec.Priority
	}

	var queued, running []Slot
	byKey := map[string]*batchv1.Job{}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if done, _, _ := jobFinished(job); done || !job.DeletionTimestamp.IsZero() {
			continue
		}
		slot := Slot{Hardware: job.Labels[LabelHardware], Profile: job.Annotations[AnnotationProfile]}
		if !jobSuspended(job) {
			slot.Key = job.Namespace + "/" + job.Name
			running = append(running, slot)
			continue
		}
		ref := metav1.GetControllerOf(job)
		if ref == nil {
			continue
		}
		owner, ok := owners[ref.UID]
		if !ok || terminal(owner.Status.Phase) || (owner.Spec.Suspend != nil && *owner.Spec.Suspend) {
			continue
		}
		slot.Key = owner.Namespace + "/" + owner.Name
		slot.Priority = owner.Spec.Priority
		if slot.Priority == 0 {
			slot.Priority = profilePriority[owner.Spec.ProfileRef]
		}
		slot.Created = owner.CreationTimestamp.Time
		queued = append(queued, slot)
		byKey[slot.Key] = job
	}

	admitted := Admit(queued, running, Budget{Slots: r.Slots, ProfileLimits: r.ProfileLimits})
	setActive(r.Slots, running, admitted)
	var errs []error
	for _, s := range admitted {
		job := byKey[s.Key]
		if err := r.setSuspend(ctx, job, false); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Info("admitted transcode", "transcodeJob", s.Key, "job", job.Name, "hardware", s.Hardware,
			"priority", s.Priority)
	}
	return errors.Join(errs...)
}

// setSuspend flips spec.suspend on a Job with a merge patch of that one
// field. Not server-side apply: an apply configuration holding only
// spec.suspend would, under the manager that created the Job, release the
// whole pod template it no longer declares.
func (r *Reconciler) setSuspend(ctx context.Context, job *batchv1.Job, suspend bool) error {
	orig := job.DeepCopy()
	job.Spec.Suspend = ptr.To(suspend)
	if err := r.Client.Patch(ctx, job, client.MergeFrom(orig), client.FieldOwner(k8s.ManagerSquasharr.String())); err != nil {
		return fmt.Errorf("transcodejob: set suspend=%t on Job %s/%s: %w", suspend, job.Namespace, job.Name, err)
	}
	return nil
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

// controllerView is the part of a status this manager owns, for the
// "anything to write?" comparison.
func controllerView(st transcodev1alpha1.TranscodeJobStatus) transcodev1alpha1.TranscodeJobStatus {
	st.Progress, st.Result, st.StderrTail = nil, nil, ""
	return st
}

// apply writes desired as ONE complete ControllerFields declaration. The
// seed is desired itself -- the live status plus this pass's changes -- so
// every field this manager owns is sent whichever path got here.
// Conditions are rendered once, in full: squasharr/status seeds none of
// them, and the generated WithConditions appends.
func (r *Reconciler) apply(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, desired *transcodev1alpha1.TranscodeJobStatus) error {
	if equality.Semantic.DeepEqual(controllerView(tj.Status), controllerView(*desired)) {
		return nil
	}
	sort.SliceStable(desired.Conditions, func(i, j int) bool { return desired.Conditions[i].Type < desired.Conditions[j].Type })
	seed := tj.DeepCopy()
	seed.Status = *desired
	return squasharrstatus.Patch(ctx, r.Client, k8s.ManagerSquasharr, seed,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			if len(desired.Conditions) > 0 {
				ac.WithConditions(k8s.ConditionACs(desired.Conditions)...)
			}
		})
}

// jobSignal is what, on an owned Job, is worth waking for: the suspend flag
// (admission flipped it), the pod counters and the terminal conditions.
// Job status is written by the Job controller, never by this reconciler, so
// a generation predicate would never fire on it -- the D2-8a trap.
func jobSignal(o client.Object) string {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return ""
	}
	var conds []string
	for _, c := range j.Status.Conditions {
		if c.Status == "True" {
			conds = append(conds, string(c.Type))
		}
	}
	sort.Strings(conds)
	return fmt.Sprintf("suspend=%t active=%d failed=%d succeeded=%d started=%t conds=%s",
		jobSuspended(j), j.Status.Active, j.Status.Failed, j.Status.Succeeded, j.Status.StartTime != nil,
		strings.Join(conds, ","))
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

// SetupWithManager registers the TranscodeJob controller.
//
// What wakes it, and why each is needed:
//   - TranscodeJob create and spec change (spec.suspend, spec.priority).
//     NOT its own status: the worker writes progress every ~10s and that is
//     none of this controller's business.
//   - An owned Job's suspend flag, pod counters or terminal conditions
//     ([jobSignal]). The Job controller writes those as status, which never
//     moves metadata.generation, so a generation predicate here would leave
//     every TranscodeJob stuck in Running forever.
//   - A TranscodeProfile's spec or status.hash ([profileSignal]), mapped to
//     its non-terminal jobs, for a job Pending on the hash.
//   - A MediaFile's probe ([mediaFileSignal]), mapped to its non-terminal
//     jobs, for a job Pending on the probe.
//
// MaxConcurrentReconciles is pinned to 1: admission counts slots then
// unsuspends, and two concurrent passes could both see the same free slot.
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

	return ctrl.NewControllerManagedBy(mgr).
		Named("transcodejob").
		For(&transcodev1alpha1.TranscodeJob{}, builder.WithPredicates(k8s.GenerationChanged())).
		Owns(&batchv1.Job{}, builder.WithPredicates(k8s.StatusFieldChanged(jobSignal))).
		Watches(&transcodev1alpha1.TranscodeProfile{},
			handler.EnqueueRequestsFromMapFunc(r.mapIndexed(indexProfileRef, false)),
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(profileSignal)))).
		Watches(&catalogv1alpha1.MediaFile{},
			handler.EnqueueRequestsFromMapFunc(r.mapIndexed(indexMediaFileRef, true)),
			builder.WithPredicates(k8s.StatusFieldChanged(mediaFileSignal))).
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
