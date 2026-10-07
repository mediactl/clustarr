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

package transcodeprofile

import (
	"context"
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// A GPU pool with a pod unschedulable for longer than unschedulableAfter is
// marked unschedulable for unschedulableFor: admission sends it no auto or
// gpu file and reroutes its Queued ones (spec §18.5).
const (
	unschedulableAfter = 10 * time.Minute
	unschedulableFor   = 30 * time.Minute
)

// A Failed pool is recreated after poolBackoffMin, doubling on each failure
// that follows the last wait closely, up to poolBackoffMax (spec §7).
const (
	poolBackoffMin = time.Minute
	poolBackoffMax = 30 * time.Minute
)

// pools is §5.12 steps 2-4 for tp: its pool Jobs read through the
// APIReader (a pool created a moment ago must not read as missing),
// unschedulable GPU pools marked in the ledger, and each pool applied from
// pool.Next over the ledger's confirmed dispatches -- none of it before the
// ledger is rebuilt, when an empty ledger would suspend every running pool
// and kill every in-flight encode. It returns when to look again.
func (r *Reconciler) pools(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile) (time.Duration, error) {
	stored, err := r.poolJobs(ctx, tp)
	if err != nil {
		return 0, err
	}
	var requeue time.Duration
	if r.markUnschedulable(ctx, tp, stored) {
		requeue = scheduleRecheck
	}
	var errs []error
	for _, class := range pool.Classes {
		k := pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class}
		n, ok := r.Ledger.DispatchedPerPool(k)
		if !ok {
			return waitForRebuild, nil // the pool waits for the rebuild (§5.12)
		}
		name := pool.Name(k)
		cur := stored[name]
		st := r.Ledger.PoolState(k)
		if cur == nil && st.Recreate {
			r.Ledger.UpdatePool(k, func(p *admission.PoolState) { p.Recreate = false })
			st.Recreate = false
		}
		if cur != nil && pool.OwnerProfileUID(cur) != tp.UID {
			// Not ours to apply to or delete: admission holds its work,
			// saying so (admission.Holding).
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: the pool's Job is not owned by its TranscodeProfile; leaving it alone",
				"pool", name, "profile", tp.Name)
			continue
		}
		want := pool.Want(tp, class, r.Pool)
		drift := pool.DriftNone
		switch {
		case cur == nil:
		case st.Recreate:
			drift = pool.DriftRecreate
		default:
			drift = pool.Classify(cur, want)
		}
		d, act := pool.Next(cur, n, drift)
		switch act {
		case pool.ActionDelete:
			why := "drained for recreation after a profile change"
			if pool.Failed(cur) {
				why = "failed"
			}
			errs = append(errs, r.deletePool(ctx, k, tp, cur, why))
		case pool.ActionApply:
			if cur == nil && r.now().Before(st.BackoffUntil) {
				requeue = shorter(requeue, st.BackoffUntil.Sub(r.now()))
				continue // a Failed pool waits out its backoff; its tasks stay queued
			}
			errs = append(errs, r.applyPool(ctx, k, tp, want, d, cur, n))
		}
		if cur != nil && class != transcodev1alpha1.HardwareCPU && cur.Status.Active > ptr.Deref(cur.Status.Ready, 0) {
			requeue = shorter(requeue, scheduleRecheck)
		}
	}
	return requeue, errors.Join(errs...)
}

// poolJobs is tp's pool Jobs by name, through the APIReader.
func (r *Reconciler) poolJobs(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile) (map[string]*batchv1.Job, error) {
	var jobs batchv1.JobList
	opts := []client.ListOption{client.MatchingLabels{pool.LabelManagedBy: pool.ManagedByValue, pool.LabelProfile: pool.ProfileLabelValue(tp.Name)}}
	if r.Pool.Namespace != "" {
		opts = append(opts, client.InNamespace(r.Pool.Namespace))
	}
	if err := r.APIReader.List(ctx, &jobs, opts...); err != nil {
		return nil, fmt.Errorf("transcodeprofile: list pool Jobs: %w", err)
	}
	out := make(map[string]*batchv1.Job, len(jobs.Items))
	for i := range jobs.Items {
		out[jobs.Items[i].Name] = &jobs.Items[i]
	}
	return out, nil
}

// markUnschedulable marks in the ledger every GPU pool of tp with a pod that
// has waited unschedulable for longer than unschedulableAfter, records a
// Warning Event once per mark, and wakes admission, which reroutes the
// pool's Queued auto files (§5.12 step 3). It reports whether any GPU pool
// still has a pod not Ready.
func (r *Reconciler) markUnschedulable(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile, stored map[string]*batchv1.Job) bool {
	pending := false
	now := r.now()
	for _, class := range admission.GPUClasses {
		k := pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class}
		j := stored[pool.Name(k)]
		if j == nil || ptr.Deref(j.Spec.Suspend, false) || pool.OwnerProfileUID(j) != tp.UID || j.Status.Active <= ptr.Deref(j.Status.Ready, 0) {
			continue
		}
		pending = true
		var pods corev1.PodList
		if err := r.APIReader.List(ctx, &pods, client.InNamespace(j.Namespace), client.MatchingLabels{
			batchv1.JobNameLabel: j.Name, batchv1.ControllerUidLabel: string(j.UID),
		}); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: cannot list a pool's pods", "pool", j.Name, "error", err)
			continue
		}
		since := unschedulableSince(pods.Items)
		if since.IsZero() || now.Sub(since) <= unschedulableAfter {
			continue
		}
		if now.Before(r.Ledger.PoolState(k).UnschedulableUntil) {
			continue
		}
		until := now.Add(unschedulableFor)
		r.Ledger.SetUnschedulable(k, until)
		logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: a GPU pool cannot schedule its pods; its auto files go to cpu",
			"pool", j.Name, "unschedulableSince", since, "until", until)
		if r.Recorder != nil {
			r.Recorder.Eventf(tp, nil, corev1.EventTypeWarning, ReasonPoolUnschedulable, "Reroute",
				"pool Job %s has had a pod unschedulable since %s; its queued auto files go to cpu, and none is sent to it until %s",
				j.Name, since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
		}
	}
	return pending
}

// unschedulableSince is when the longest-waiting live pod of pods began to
// wait unschedulable (PodScheduled=False, reason Unschedulable), or the zero
// time when none is.
func unschedulableSince(pods []corev1.Pod) time.Time {
	var since time.Time
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodPending {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse &&
				c.Reason == corev1.PodReasonUnschedulable && !c.LastTransitionTime.IsZero() &&
				(since.IsZero() || c.LastTransitionTime.Time.Before(since)) {
				since = c.LastTransitionTime.Time
			}
		}
	}
	return since
}

// applyPool renders and applies one pool's desired shape d under
// transcode-pool: pool.Render's complete declaration, re-sending a running
// Job's recorded template (CLAUDE.md: a running Job's template is re-sent,
// not re-rendered).
func (r *Reconciler) applyPool(ctx context.Context, k pool.Key, tp *transcodev1alpha1.TranscodeProfile,
	want pool.Spec, d pool.Desired, cur *batchv1.Job, n int32,
) error {
	log := logging.FromContext(ctx)
	name := pool.Name(k)
	ac, err := pool.Render(k, tp, want, d, cur, r.Pool)
	if errors.Is(err, pool.ErrNoAppliedSpec) {
		// Ruling R15: the Job lost its applied-template annotation; Next asks
		// to suspend it only with nothing dispatched, so recreate it now.
		return r.deletePool(ctx, k, tp, cur, "lost its applied-template annotation")
	}
	if err != nil {
		return fmt.Errorf("transcodeprofile: render pool %s: %w", name, err)
	}
	_, err = k8s.Apply(ctx, r.Client, k8s.ManagerTranscodePool, ac)
	if cur != nil && pool.IsRecreateOnly(err) {
		// Created with something the apiserver never lets change: recreated
		// now when nothing runs there, else drained first (spec §7).
		if n == 0 || pool.Mutable(cur) {
			return r.deletePool(ctx, k, tp, cur, "created with a spec the apiserver will not change")
		}
		r.Ledger.UpdatePool(k, func(p *admission.PoolState) { p.Recreate = true })
		log.InfoContext(ctx, "transcodeprofile: the pool was created with a spec the apiserver will not change; draining it for recreation",
			"pool", name, "dispatched", n, "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("transcodeprofile: apply pool %s: %w", name, err)
	}
	log.InfoContext(ctx, "transcodeprofile: applied pool", "pool", name, "parallelism", d.Parallelism, "suspend", d.Suspend, "dispatched", n)
	return nil
}

// deletePool deletes a pool Job so the next pass with work recreates it:
// background propagation, conditional on its UID. A Failed pool also gets a
// Warning Event and a recreation backoff in the ledger (spec §7).
func (r *Reconciler) deletePool(ctx context.Context, k pool.Key, tp *transcodev1alpha1.TranscodeProfile, j *batchv1.Job, why string) error {
	err := r.Client.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground), client.Preconditions{UID: new(j.UID)})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("transcodeprofile: delete pool %s: %w", j.Name, err)
	}
	failed := pool.Failed(j)
	now := r.now()
	var delay time.Duration
	r.Ledger.UpdatePool(k, func(p *admission.PoolState) {
		p.Recreate = false
		if !failed {
			return
		}
		delay = poolBackoffMin
		if !p.BackoffUntil.IsZero() && now.Sub(p.BackoffUntil) < poolBackoffMax {
			delay = min(p.BackoffDelay*2, poolBackoffMax)
		}
		p.FailedAt, p.BackoffUntil, p.BackoffDelay = admission.FailedAt(j, now), now.Add(delay), delay
	})
	log := logging.FromContext(ctx)
	if !failed {
		log.InfoContext(ctx, "transcodeprofile: deleted pool for recreation", "pool", j.Name, "reason", why)
		return nil
	}
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			why = fmt.Sprintf("%s: %s", c.Reason, c.Message)
		}
	}
	log.WarnContext(ctx, "transcodeprofile: pool failed; recreating it after a backoff", "pool", j.Name, "reason", why, "backoff", delay)
	if r.Recorder != nil {
		r.Recorder.Eventf(tp, nil, corev1.EventTypeWarning, ReasonPoolFailed, "RecreatePool",
			"pool Job %s failed (%s); it is recreated in %s", j.Name, why, delay)
	}
	return nil
}
