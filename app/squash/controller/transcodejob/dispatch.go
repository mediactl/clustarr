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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/version"
)

// classFor is the class a Planned job dispatches to when it does not choose
// one per dispatch -- a pinned job, or an auto one whose plan encodes nothing
// (assignClasses gives an auto job that encodes ChooseClass's): the plan's
// encoder's class (a remux takes a CPU slot), and CPU once a fallback reason
// is recorded or with no plan to read one from (spec §18.5).
func (r *Reconciler) classFor(tj *transcodev1alpha1.TranscodeJob, _ *transcodev1alpha1.TranscodeProfile) transcodev1alpha1.Hardware {
	if tj.Status.FallbackReason != "" || tj.Status.Plan == nil {
		return transcodev1alpha1.HardwareCPU
	}
	return hardwareForEncoder(tj.Status.Plan.Encoder)
}

// poolKeyFor is the (profile, class) pool a job's task goes to.
func poolKeyFor(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) pool.Key {
	return pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class}
}

// orphaned reports whether tj is dispatched (Queued or Running) to a pool
// that no longer serves it (final-review C1): its TranscodeProfile tp is
// gone (nil), or status.jobRef is not tp's pool for status.hardware -- the
// profile was deleted and recreated under its name, a new UID and so new
// pools, while the task sits on the old UID's subject that nothing pulls.
// Every dispatch and every adoption records jobRef as exactly that pool
// (markQueued), so a live job never differs from it; a job left dispatched
// by the pre-pool controller, whose jobRef names its own batch Job, does.
func orphaned(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) bool {
	if !dispatched(tj.Status.Phase) {
		return false
	}
	if tp == nil {
		return true
	}
	return tj.Status.JobRef == nil || *tj.Status.JobRef != pool.Name(poolKeyFor(tp, tj.Status.Hardware))
}

// dispatch publishes one admitted job's task, then records it: a job is never
// Queued without a task on the queue (spec §8). The status write is
// conditional on the attempt count the task was built from, so a job cannot
// be recorded as dispatched twice; a task published twice for one attempt
// carries one Msg-Id, which the stream's duplicate window absorbs.
//
// A job whose recorded plan is for another class than the one admission
// chose is planned again for it first (spec §18.5: an auto job's plan is
// made for the class it is sent to), and the task carries the new plan's
// argsHash; the Queued write records that plan, in the same write. When the
// new plan is a skip or a reject, or fails, it is recorded as plan records
// it and nothing is published. When it needs another class than the chosen
// one -- a Dolby Vision source, which no hardware encoder writes, planned for
// a GPU -- the job stays Planned with that plan and, for an auto job, a
// fallbackReason, so the next pass sends it to cpu.
//
// A source or output under no RootFolder can never be dispatched: it is
// blocked here, as InvalidSource, without costing a pod (spec §17.5).
func (r *Reconciler) dispatch(ctx context.Context, key types.NamespacedName, class transcodev1alpha1.Hardware) error {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.dispatch")
	defer span.End()

	if r.Bus == nil {
		return errors.New("transcodejob: no bus to dispatch on")
	}
	var tj transcodev1alpha1.TranscodeJob
	if err := r.reader().Get(ctx, key, &tj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || k8s.IsDeleting(&tj) ||
		(tj.Spec.Suspend != nil && *tj.Spec.Suspend) {
		return nil // moved on, going, or paused since admission listed it
	}
	tp, ok, err := r.profile(ctx, &tj)
	if err != nil || !ok {
		return err
	}
	if tp.Status.Hash == "" {
		// The worker tags its output "<profile>@<hash>"; a task without the
		// hash would write a tag nothing recognises as done. The profile
		// controller hashes every profile it reconciles.
		return fmt.Errorf("transcodejob: TranscodeProfile %s has no status.hash yet", tp.Name)
	}
	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			// The job is owned by its MediaFile: garbage collection is
			// already deleting it, and there is nothing to transcode.
			return nil
		}
		return fmt.Errorf("transcodejob: get MediaFile %s: %w", tj.Spec.MediaFileRef, err)
	}
	// Uncached: RootFolders are read only here, and a cached List would
	// start an informer this controller's role has no watch for.
	var folders catalogv1alpha1.RootFolderList
	if err := r.reader().List(ctx, &folders, client.InNamespace(tj.Namespace)); err != nil {
		return fmt.Errorf("transcodejob: list RootFolders: %w", err)
	}

	var replanned *planning
	if tj.Status.Plan == nil || hardwareForEncoder(tj.Status.Plan.Encoder) != class {
		p, fail := planFor(&tj, tp, &mf, &class)
		if fail != nil || p.result.Decision == transcode.DecisionSkip || p.result.Decision == transcode.DecisionReject ||
			hardwareForEncoder(encoderName(p.result)) != class {
			return r.keepPlanned(ctx, key, tj.Status.Attempts, tp, class, p, fail)
		}
		replanned = &p
		tj.Status.Plan = statusPlan(p.result) // the task carries this plan's argsHash
	}

	attempt := tj.Status.Attempts + 1
	t, buildErr := worker.BuildTask(&tj, tp, &mf, folders.Items, attempt, class)
	if errors.Is(buildErr, worker.ErrNoRootFolder) || errors.Is(buildErr, worker.ErrInvalidOutput) {
		before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
			if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned {
				return false
			}
			now := r.now().Time
			applyDecision(tj, st, task.StatusEvent{At: now}, Decision{
				Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true,
				Reason: string(task.ReasonInvalidSource), Message: "cannot dispatch: " + buildErr.Error(),
			}, now)
			return true
		})
		if after != nil {
			r.afterWrite(ctx, after, &before)
		}
		return err
	}
	if buildErr != nil {
		return fmt.Errorf("transcodejob: build task: %w", buildErr)
	}

	// The finalizer is added before publishing (spec §8): once a task exists
	// on the queue, deletion must withdraw it, never just vanish the object
	// out from under a worker that might claim it.
	//
	// A cancel marker an earlier withdrawal left on the lease is NOT cleared
	// for the new attempt. The worker's own attempt rule
	// (app/squash/worker/lease.go's claim) replaces a marker from an earlier
	// attempt and proceeds, so clearing it gains nothing -- and the marker is
	// what still stops a delivery of the withdrawn attempt that a worker
	// fetched before the purge and has yet to claim. A job taken back from an
	// unschedulable GPU pool is dispatched again in the same admission pass
	// (pools.go, reroute), milliseconds after its withdrawal, so clearing it
	// here would let that late claim run the withdrawn attempt beside the new
	// one.
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &tj, FinalizerTaskWithdrawal); err != nil {
		return fmt.Errorf("transcodejob: add the withdrawal finalizer: %w", err)
	}

	sch, data, err := schema.Encode(t)
	if err != nil {
		return err
	}
	id := events.MsgIDForTranscodeTask(string(tj.UID), attempt)
	now := r.now().Time
	env := &events.Envelope{
		ID: id, Type: "transcode.Task", Schema: sch, Source: "squasharr-controller@" + version.String(),
		Key: tj.Namespace + "/" + tj.Name, Time: now, Data: data,
	}
	// The worker's spans continue the trace of the reconcile that
	// dispatched it, as the Job's CLUSTARR_TRACEPARENT once carried.
	tracing.Inject(ctx, env)
	k := poolKeyFor(tp, class)
	subject := events.WorkTranscodeTaskSubject(string(tp.UID), string(class), string(tj.UID))
	if _, pubErr := r.Bus.Publish(ctx, subject, env,
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr)); pubErr != nil {
		// Nothing is marked Queued that was not published (spec §8): the
		// job stays Planned, saying why, and the next admission pass tries
		// again.
		msg := fmt.Sprintf("dispatch to pool %s failed: %v", pool.Name(k), pubErr)
		if _, _, err := r.writeStatus(ctx, key, func(_ *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
			if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned {
				return false
			}
			st.Message = truncate(msg, maxMessage)
			return true
		}); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodejob: could not record the dispatch failure",
				"transcodeJob", key.String(), "error", err)
		}
		return fmt.Errorf("transcodejob: publish task %s: %w", id, pubErr)
	}

	// If this write is lost (an apiserver blip, a rollout cancelling ctx,
	// leadership moving), the task is on the queue while the job still reads
	// Planned: the worker's first event for this attempt adopts it
	// (results.go), and until then a re-dispatch republishes under the same
	// Msg-Id, which the stream absorbs.
	before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || st.Attempts != attempt-1 {
			return false // someone else moved it; the published task is a duplicate the Msg-Id absorbs
		}
		if replanned != nil {
			r.recordPlan(tj, st, *replanned, tp.Spec.Container) // the plan the task was built from
		}
		markQueued(tj, st, attempt, class, pool.Name(k))
		return true
	})
	if after != nil {
		r.afterWrite(ctx, after, &before)
	}
	return err
}

// keepPlanned records what planning the job again for class decided when it
// cannot be dispatched to class, and publishes nothing: a planning failure
// Fails the job and a skip or reject Skips it, as plan records each; a plan
// that needs another class is recorded, with -- for an auto job -- a
// fallbackReason that sends it to cpu from the next pass on, which it wakes.
// The write is conditional on the job still being Planned at attempts, as
// dispatch read it.
func (r *Reconciler) keepPlanned(ctx context.Context, key types.NamespacedName, attempts int32,
	tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, p planning, fail *planFailure,
) error {
	fellBack := false
	before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		fellBack = false // this may run again, from a fresh read, after a Conflict
		if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || st.Attempts != attempts {
			return false
		}
		if fail != nil {
			r.fail(tj, st, fail.reason, "%s", fail.msg)
			return true
		}
		r.recordPlan(tj, st, p, tp.Spec.Container)
		if st.Phase == transcodev1alpha1.TranscodeJobPhasePlanned && isAutoFor(tj, tp) {
			st.FallbackReason = truncate(fmt.Sprintf("a plan for %s encodes with %s, which needs no GPU", class, st.Plan.Encoder),
				maxFallbackReason)
			st.Message = truncate(st.FallbackReason+"; it goes to cpu", maxMessage)
			fellBack = true
		}
		return true
	})
	if after != nil {
		r.afterWrite(ctx, after, &before)
	}
	if fellBack && after != nil {
		r.wakeAdmission() // the slot this pass gave it is free, and the job wants a cpu one
	}
	return err
}

// markQueued records attempt as dispatched to class's pool, poolName (empty
// leaves jobRef as it was). It is the one Queued mutation: dispatch makes it
// after its publish, and the results consumer makes it when a worker's
// event proves an attempt was published whose Queued write was lost.
func markQueued(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus,
	attempt int32, class transcodev1alpha1.Hardware, poolName string,
) {
	st.Phase, st.Attempts, st.Hardware = transcodev1alpha1.TranscodeJobPhaseQueued, attempt, class
	st.WorkerPod, st.NextAttemptAt, st.Progress = "", nil, nil
	st.Message = fmt.Sprintf("attempt %d queued", attempt)
	if poolName != "" {
		st.JobRef = ptr.To(poolName)
		st.Message = fmt.Sprintf("attempt %d queued for pool %s", attempt, poolName)
	}
	k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonDispatched, "%s", st.Message)
}

// poolNameFor is the pool Job name of tj's profile for class, or "" when
// the profile is gone.
func (r *Reconciler) poolNameFor(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, class transcodev1alpha1.Hardware) (string, error) {
	tp, ok, err := r.profile(ctx, tj)
	if err != nil || !ok {
		return "", err
	}
	return pool.Name(poolKeyFor(tp, class)), nil
}
