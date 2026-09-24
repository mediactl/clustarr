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
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
	"github.com/mediactl/clustarr/squasharr/task"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// classFor is the class a Planned job dispatches to. Task 13 makes auto
// capacity-aware; here it is the plan's encoder's class (a remux takes a CPU
// slot), and CPU once a fallback reason is recorded (spec §18.5).
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

// dispatch publishes one admitted job's task, then records it: a job is never
// Queued without a task on the queue (spec §8). The status write is
// conditional on the attempt count the task was built from, so a job cannot
// be recorded as dispatched twice; a task published twice for one attempt
// carries one Msg-Id, which the stream's duplicate window absorbs.
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

	before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || st.Attempts != attempt-1 {
			return false // someone else moved it; the published task is a duplicate the Msg-Id absorbs
		}
		st.Phase, st.Attempts, st.Hardware = transcodev1alpha1.TranscodeJobPhaseQueued, attempt, class
		st.JobRef, st.WorkerPod, st.NextAttemptAt, st.Progress = ptr.To(pool.Name(k)), "", nil, nil
		st.Message = fmt.Sprintf("attempt %d queued for pool %s", attempt, pool.Name(k))
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonDispatched, "%s", st.Message)
		return true
	})
	if after != nil {
		r.afterWrite(ctx, after, &before)
	}
	return err
}
