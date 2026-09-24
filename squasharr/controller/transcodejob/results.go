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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr/task"
)

// ResultsConsumer subscribes squasharr-transcode-results. It runs on the
// leader only, beside the reconciler it shares the one status write path
// with (spec §18.2).
func (r *Reconciler) ResultsConsumer() manager.Runnable { return resultsConsumer{r} }

type resultsConsumer struct{ r *Reconciler }

// Start subscribes the consumer and blocks until ctx ends.
func (c resultsConsumer) Start(ctx context.Context) error {
	if c.r.Bus == nil {
		return errors.New("squasharr: the results consumer has no bus")
	}
	spec, ok := events.Default().Consumer(events.ConsumerSquasharrResults)
	if !ok {
		return fmt.Errorf("squasharr: %s is missing from the topology", events.ConsumerSquasharrResults)
	}
	stop, err := c.r.Bus.Subscribe(ctx, spec.Subscription(), c.r.handleEvent)
	if err != nil {
		return fmt.Errorf("squasharr: subscribe %s: %w", spec.Name, err)
	}
	<-ctx.Done()
	stop()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: one writer of
// the status these events describe, the leader's.
func (resultsConsumer) NeedLeaderElection() bool { return true }

// handleEvent turns one worker status event into status and, for finished,
// the next step (spec §18.2, §18.3). It returns nil -- ack -- once the write
// the event causes has landed, or when the event no longer applies: one for
// another incarnation of the job, another attempt, or a job that is not
// dispatched (Queued or Running) any more is stale or a duplicate and changes
// nothing. Any other error naks it, to be redelivered on the consumer's
// backoff.
func (r *Reconciler) handleEvent(ctx context.Context, m events.Message) error {
	ctx, span := tracing.Start(ctx, "transcodejob.Reconciler.handleEvent")
	defer span.End()

	var ev task.StatusEvent
	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &ev); err != nil {
		return events.Discard("undecodable transcode status event", err)
	}
	if ev.Job.Name == "" || ev.Job.Namespace == "" {
		return events.Discard("transcode status event names no TranscodeJob", nil)
	}
	key := types.NamespacedName{Namespace: ev.Job.Namespace, Name: ev.Job.Name}
	ctx = logging.With(ctx, "transcodeJob", key.String(), "attempt", ev.Attempt, "event", string(ev.Kind))

	var (
		decideErr        error
		adopted, noClass bool
	)
	before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		adopted, noClass = false, false // this may run again, from a fresh read, after a Conflict
		if string(tj.UID) != ev.Job.UID || k8s.IsDeleting(tj) {
			return false // another incarnation of the job, or one going away
		}
		if st.Phase == transcodev1alpha1.TranscodeJobPhasePlanned && ev.Attempt == st.Attempts+1 {
			// A worker is running the attempt after the job's last recorded
			// one, so dispatch published it and then lost its Queued write
			// (R16). Adopt the attempt from the event, in this same write,
			// rather than drop the worker's report and leave a job that the
			// next dispatch would mark Queued with no task anywhere.
			if ev.Class == "" {
				noClass = true // a worker from before events carried their class
				return false
			}
			poolName, err := r.poolNameFor(ctx, tj, ev.Class)
			if err != nil {
				decideErr = err
				return false
			}
			markQueued(tj, st, ev.Attempt, ev.Class, poolName)
			adopted = true
		}
		if st.Attempts != ev.Attempt || !dispatched(st.Phase) {
			return false // stale, duplicate, or for a job that moved on
		}
		at := ev.At
		if at.IsZero() {
			at = r.now().Time
		}
		switch ev.Kind {
		case task.EventClaimed, task.EventProgress:
			if ev.Class != "" && ev.Class != st.Hardware {
				// A re-dispatch to another class was absorbed as a duplicate
				// of this attempt's first publish: the task is where the
				// worker took it from.
				poolName, err := r.poolNameFor(ctx, tj, ev.Class)
				if err != nil {
					decideErr = err
					return false
				}
				st.Hardware = ev.Class
				if poolName != "" {
					st.JobRef = &poolName
				}
			}
			if st.Phase != transcodev1alpha1.TranscodeJobPhaseRunning {
				st.Phase = transcodev1alpha1.TranscodeJobPhaseRunning
				st.Message = fmt.Sprintf("attempt %d running", st.Attempts)
			}
			if ev.Pod != "" {
				st.WorkerPod = truncate(ev.Pod, maxWorkerPod)
				st.Message = fmt.Sprintf("attempt %d running on %s", st.Attempts, st.WorkerPod)
			}
			if st.StartedAt == nil {
				st.StartedAt = &metav1.Time{Time: at}
			}
			if ev.Progress != nil {
				p := *ev.Progress
				if p.UpdatedAt.IsZero() {
					p.UpdatedAt = metav1.Time{Time: at}
				}
				// Events of one delivery arrive in order, but a nak'd one
				// is redelivered behind its successors: never step back.
				if st.Progress == nil || !p.UpdatedAt.Before(&st.Progress.UpdatedAt) {
					st.Progress = &p
				}
			}
			return true
		case task.EventFinished:
			auto, err := r.isAuto(ctx, tj)
			if err != nil {
				decideErr = err
				return false
			}
			d := Decide(ev, *st, auto)
			if d.NoOp && adopted {
				// Cancelled: the withdrawal acted on this attempt, and its
				// worker acked the task. Recording it Queued now would leave a
				// dispatched job with no task behind it.
				return false
			}
			applyDecision(tj, st, ev, d, r.now().Time)
			return adopted || !d.NoOp || ev.StderrTail != ""
		}
		return adopted
	})
	log := logging.FromContext(ctx)
	if decideErr != nil {
		tracing.RecordError(span, decideErr)
		return decideErr
	}
	if apierrors.IsNotFound(err) {
		return nil // the job is gone; nothing is left to describe
	}
	if apierrors.IsInvalid(err) {
		// The apiserver will refuse this write however often it is retried,
		// and the consumer settles one event at a time (MaxAckPending 1):
		// retrying would hold every other job's status back behind it
		// until it dead-lettered anyway (R17).
		log.WarnContext(ctx, "squasharr: a transcode status event makes an invalid status; dead-lettering it",
			"error", err)
		return events.Discard("the status this event makes is invalid", err)
	}
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}
	if noClass {
		log.WarnContext(ctx, "squasharr: an event for an attempt the job never recorded carries no class, so it cannot be adopted; dropping it",
			"recordedAttempts", before.Attempts)
	}
	if adopted && after != nil {
		log.InfoContext(ctx, "squasharr: adopted an attempt whose dispatch write was lost, from its worker's event",
			"hardware", string(after.Status.Hardware))
	}
	if after != nil {
		r.afterWrite(ctx, after, &before)
		if dispatched(before.Phase) && !dispatched(after.Status.Phase) {
			r.wakeAdmission() // a slot is free, or a requeued job wants one
		}
	}
	return nil
}

// dispatched reports whether a job in phase p holds a slot: its task is on a
// pool's queue or a worker has it.
func dispatched(p transcodev1alpha1.TranscodeJobPhase) bool {
	return p == transcodev1alpha1.TranscodeJobPhaseQueued || p == transcodev1alpha1.TranscodeJobPhaseRunning
}

// isAuto reports whether tj chooses its class per dispatch (spec §18.5):
// isAutoFor, reading the profile only when the job's own spec.hardware does
// not decide. A profile that cannot be read is an error, not a guess: the
// answer decides between a CPU fallback and a retry.
func (r *Reconciler) isAuto(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) (bool, error) {
	if tj.Spec.Hardware != nil && *tj.Spec.Hardware != "" {
		return isAutoFor(tj, nil), nil
	}
	tp, ok, err := r.profile(ctx, tj)
	if err != nil {
		return false, err
	}
	// A deleted profile pins nothing it can be asked about; retrying on the
	// class the attempt used is the conservative answer.
	if !ok {
		return false, nil
	}
	return isAutoFor(tj, tp), nil
}
