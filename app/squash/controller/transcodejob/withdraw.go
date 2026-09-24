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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// FinalizerTaskWithdrawal blocks a TranscodeJob's deletion until its
// dispatched task, if any, has been withdrawn (spec §8): a cancelled lease
// reaches a worker at its next renewal, and the purge removes a task no
// worker has taken yet. dispatch.go adds it before publishing a task;
// afterWrite removes it once a write makes the job terminal, and
// reconcileDelete removes it once withdrawal has succeeded or timed out.
const FinalizerTaskWithdrawal = "squasharr.clustarr.io/task-withdrawal"

// withdrawalTimeout bounds how long deletion waits for withdrawal to
// succeed before releasing the finalizer anyway -- the R-6 pattern: a NATS
// outage must not pin a TranscodeJob forever.
const withdrawalTimeout = 10 * time.Minute

// sweepInterval is how often admit's backstop sweep looks for task subjects
// with no live TranscodeJob behind them.
const sweepInterval = 5 * time.Minute

// deleteRequeue is how soon deletion is reconciled again while withdrawal
// keeps failing (an unreachable bus): short enough that a recovered NATS is
// noticed quickly, long enough not to hot-loop one.
const deleteRequeue = 30 * time.Second

// ReasonWithdrawalTimedOut is the Warning Event reason recorded when the
// withdrawal finalizer is released without a confirmed withdrawal.
const ReasonWithdrawalTimedOut = "WithdrawalTimedOut"

// ReasonSuspended is JobCreated=False's reason on a job withdrawn and
// returned to Planned by spec.suspend, distinct from ReasonRequeued (a
// retriable failure) even though both cross the same Queued/Running ->
// Planned edge.
const ReasonSuspended = "Suspended"

// withdraw takes a dispatched job's task back (spec §8): a cancelled lease
// reaches a worker at its next renewal; the purge removes a task no worker
// has taken yet. Order matters: the marker is written first, so a worker
// that fetched the task just before the purge still finds it cancelled when
// it claims (app/squash/worker/lease.go's claim and renew).
//
// The purge needs no TranscodeProfile (ruling R23): tj.UID alone already
// identifies the subject uniquely, so it is purged with a wildcard in the
// profile's place (events.WorkTranscodeTaskSubjectAnyProfile), which every
// events.StreamAdmin honours. Resolving the profile first -- as an earlier
// version of this function did -- was the bug R23 fixed: a job whose
// profile had been deleted skipped the purge (nil profile) or, on a merely
// transient read error, wrongly reported success (a "gone" and an
// "unreadable" profile look identical to a bare two-value read); either way
// a suspended or deleted job's task never left the stream. It is DiscardNew
// with no MaxAge, so a leaked task never expires on its own and only grows
// the stream toward its cap. A never-dispatched job (status.hardware=="")
// still has nothing to purge.
func (r *Reconciler) withdraw(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) error {
	uid := string(tj.UID)
	b, err := json.Marshal(task.Lease{
		Job:     schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: uid},
		Attempt: tj.Status.Attempts,
		State:   task.LeaseCancelled,
		Since:   r.now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("transcodejob: encode cancelled lease: %w", err)
	}
	if _, err := r.Leases.Put(ctx, events.TranscodeLeaseKey(uid), b); err != nil {
		return fmt.Errorf("transcodejob: cancel lease: %w", err)
	}
	if tj.Status.Hardware == "" {
		return nil
	}
	if err := r.Admin.PurgeSubject(ctx, events.StreamWorkSquasharr,
		events.WorkTranscodeTaskSubjectAnyProfile(string(tj.Status.Hardware), uid)); err != nil {
		return fmt.Errorf("transcodejob: purge task: %w", err)
	}
	return nil
}

// reconcileDelete runs the withdrawal finalizer's protocol for a TranscodeJob
// marked for deletion (spec §8): cancel the lease and purge the task, then
// release the finalizer once that succeeded, or once withdrawalTimeout has
// passed since deletion was requested -- an outage must not pin the object
// forever (the R-6 pattern). It always returns, and requeues after
// deleteRequeue while withdrawal keeps failing and the timeout has not
// passed.
func (r *Reconciler) reconcileDelete(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) (ctrl.Result, error) {
	if !k8s.HasFinalizer(tj, FinalizerTaskWithdrawal) {
		return ctrl.Result{}, nil
	}
	log := logging.FromContext(ctx)
	key := client.ObjectKeyFromObject(tj)

	werr := r.withdraw(ctx, tj)
	timedOut := r.now().Sub(tj.DeletionTimestamp.Time) > withdrawalTimeout
	if werr != nil && !timedOut {
		log.WarnContext(ctx, "transcodejob: withdrawal failed; retrying", "transcodeJob", key.String(), "error", werr)
		return ctrl.Result{RequeueAfter: deleteRequeue}, nil
	}
	if werr != nil {
		log.WarnContext(ctx, "transcodejob: withdrawal timed out; releasing the finalizer anyway",
			"transcodeJob", key.String(), "error", werr)
		if r.Recorder != nil {
			r.Recorder.Eventf(tj, nil, corev1.EventTypeWarning, ReasonWithdrawalTimedOut, "Finalize",
				"withdrawal timed out; the task may still run")
		}
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, tj, FinalizerTaskWithdrawal); err != nil {
		log.WarnContext(ctx, "transcodejob: could not release the withdrawal finalizer",
			"transcodeJob", key.String(), "error", err)
		return ctrl.Result{RequeueAfter: deleteRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// releaseFinalizerIfTerminal drops the withdrawal finalizer once key's
// TranscodeJob is terminal: nothing dispatched can still be running, so
// nothing will ever need withdrawing again, and the object should delete
// instantly rather than waiting out reconcileDelete's protocol.
//
// It re-reads fresh through the uncached reader rather than trusting the
// caller's copy, so the finalizer's Update carries the resourceVersion of
// the status write that just landed -- not the one from before it, which
// would make Update conflict against the write this is called to follow.
func (r *Reconciler) releaseFinalizerIfTerminal(ctx context.Context, key types.NamespacedName) error {
	var tj transcodev1alpha1.TranscodeJob
	if err := r.reader().Get(ctx, key, &tj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !terminal(tj.Status.Phase) || !k8s.HasFinalizer(&tj, FinalizerTaskWithdrawal) {
		return nil
	}
	_, err := k8s.RemoveFinalizer(ctx, r.Client, &tj, FinalizerTaskWithdrawal)
	return err
}

// sweep purges CLUSTARR_WORK_SQUASHARR task subjects with no live
// TranscodeJob behind them (spec §8's backstop): a task published for a job
// deleted while NATS was unreachable, or whose Queued write was never
// recorded, or any other orphaned UID. tjs is every TranscodeJob that
// exists, of any phase -- a job counts as live regardless of whether it
// has ever been dispatched, since dispatch.go publishes a task before it
// records status.hardware (R16's "lost Queued write" window).
//
// It runs at most once every sweepInterval, tracked in r.nextSweep; a zero
// r.nextSweep (a fresh Reconciler) is due immediately.
func (r *Reconciler) sweep(ctx context.Context, tjs []transcodev1alpha1.TranscodeJob) error {
	if r.Admin == nil {
		return nil
	}
	now := r.now().Time
	if now.Before(r.nextSweep) {
		return nil
	}
	log := logging.FromContext(ctx)
	subjects, err := r.Admin.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")
	if err != nil {
		return fmt.Errorf("transcodejob: list task subjects for the sweep: %w", err)
	}
	live := make(map[string]bool, len(tjs))
	for i := range tjs {
		live[taskUIDToken(string(tjs[i].UID))] = true
	}
	var errs []error
	for _, subject := range subjects {
		if live[subjectUIDToken(subject)] {
			continue
		}
		if err := r.Admin.PurgeSubject(ctx, events.StreamWorkSquasharr, subject); err != nil {
			errs = append(errs, fmt.Errorf("transcodejob: sweep: purge orphaned task subject %s: %w", subject, err))
			continue
		}
		log.InfoContext(ctx, "squasharr: swept an orphaned transcode task", "subject", subject)
	}
	r.nextSweep = now.Add(sweepInterval)
	return errors.Join(errs...)
}

// taskUIDToken is jobUID's escaped subject token, exactly as
// WorkTranscodeTaskSubject renders it: the profile and class arguments are
// placeholders, since the escaping of one token never depends on another,
// and the one builder is left to own the escaping rather than restating it.
func taskUIDToken(jobUID string) string {
	return subjectUIDToken(events.WorkTranscodeTaskSubject("", "", jobUID))
}

// subjectUIDToken is a WorkTranscodeTaskSubject subject's last token.
func subjectUIDToken(subject string) string {
	return subject[strings.LastIndex(subject, ".")+1:]
}
