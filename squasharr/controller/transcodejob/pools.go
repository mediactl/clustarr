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
	"maps"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
)

// ReasonPoolFailed is the Warning Event reason on a TranscodeProfile whose
// pool Job failed and is recreated.
const ReasonPoolFailed = "PoolFailed"

// A Failed pool is recreated after poolBackoffMin, doubling on each failure
// that follows the last wait closely, up to poolBackoffMax (spec §7).
const (
	poolBackoffMin = time.Minute
	poolBackoffMax = 30 * time.Minute
)

// heldPrefix starts the message of a Planned job admission holds for a
// draining pool, so a later pass can tell the message is its own.
const heldPrefix = "waiting for pool "

// poolBackoff is one pool's recreation backoff: no create before until, and
// delay is the wait that until was set with, so the next one can double it.
// It is kept in memory: a restart forgets it, which only shortens one wait.
type poolBackoff struct {
	until time.Time
	delay time.Duration
}

// poolJobs lists every pool Job, by name, through the uncached reader: a
// pool created a moment ago must not read as missing, or the next pass would
// render it as new.
func (r *Reconciler) poolJobs(ctx context.Context) (map[string]*batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := r.reader().List(ctx, &jobs, client.InNamespace(r.Pool.Namespace),
		client.MatchingLabels{pool.LabelManagedBy: pool.ManagedByValue}, client.HasLabels{pool.LabelProfile}); err != nil {
		return nil, fmt.Errorf("transcodejob: list pool Jobs: %w", err)
	}
	out := make(map[string]*batchv1.Job, len(jobs.Items))
	for i := range jobs.Items {
		out[jobs.Items[i].Name] = &jobs.Items[i]
	}
	return out, nil
}

// ownerProfileUID is the UID of the TranscodeProfile that is j's controller
// owner, or "" when none is.
func ownerProfileUID(j *batchv1.Job) types.UID {
	if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "TranscodeProfile" &&
		strings.HasPrefix(ref.APIVersion, transcodev1alpha1.GroupVersion.Group+"/") {
		return ref.UID
	}
	return ""
}

// drift is how far the stored pool cur is from want: [pool.Classify], or
// DriftRecreate for a pool the apiserver refused a gang on (r.recreate).
func (r *Reconciler) drift(cur *batchv1.Job, want pool.Spec) pool.Drift {
	switch {
	case cur == nil:
		return pool.DriftNone
	case r.recreate[cur.Name]:
		return pool.DriftRecreate
	}
	return pool.Classify(cur, want)
}

// holding is the set of pools admission dispatches nothing new to in this
// pass (spec §7, "draining"): a pool whose template drifted while its pods
// may still run must finish its dispatched work and suspend before the
// change can land, and a new task would both keep it busy and run under the
// old template. It is judged before admission, from the pools as they are
// now, so the pass that first sees a profile edit already holds.
//
// A drifted pool that is suspended and stopped ([pool.Mutable]) is not
// held: the apply after dispatch reshapes and resumes it in one, or it is
// deleted and recreated for its work. Nor is a Failed pool, which is
// recreated: its tasks wait on the queue meanwhile. A pool name held by a
// Job another incarnation of the profile owns is held until garbage
// collection has removed that Job.
//
// Holding ends when the pool's dispatched jobs do, so it lasts as long as
// the slowest of them.
func (r *Reconciler) holding(stored map[string]*batchv1.Job, profiles map[string]*transcodev1alpha1.TranscodeProfile) map[pool.Key]bool {
	held := map[pool.Key]bool{}
	for name, j := range stored {
		tp, ok := profiles[j.Labels[pool.LabelProfile]]
		if !ok {
			continue
		}
		k := poolKeyFor(tp, transcodev1alpha1.Hardware(j.Labels[pool.LabelHardware]))
		if pool.Name(k) != name {
			continue // not the current profile's pool of that class
		}
		switch {
		case ownerProfileUID(j) != tp.UID:
			held[k] = true
		case pool.Failed(j) || pool.Mutable(j):
		case r.drift(j, pool.Want(tp, k.Class, r.Pool)) != pool.DriftNone:
			held[k] = true
		}
	}
	return held
}

// holdMessage is a held job's message.
func holdMessage(k pool.Key) string {
	return heldPrefix + pool.Name(k) + " to drain before its profile change applies"
}

// setHeldMessage records why admission passed tj over, or -- for a job it no
// longer holds but did not dispatch either -- replaces that record, so the
// message never claims a drain that is over. Nothing is written when the
// message already says it, or tj has moved on.
func (r *Reconciler) setHeldMessage(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, msg string) error {
	if tj.Status.Message == msg {
		return nil
	}
	_, _, err := r.writeStatus(ctx, client.ObjectKeyFromObject(tj), func(_ *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned {
			return false
		}
		st.Message = msg
		return true
	})
	return client.IgnoreNotFound(err) // deleted since the pass listed it
}

// dispatchedPerPool counts the Queued and Running jobs of each pool. The
// pool is the one status.hardware names: once a task is published, the
// class it went to is authoritative (ruling R16), whatever class admission
// would choose for the job now. So a pool is never suspended while a task
// dispatched to it is still on its queue.
func dispatchedPerPool(tjs []transcodev1alpha1.TranscodeJob, profiles map[string]*transcodev1alpha1.TranscodeProfile) map[pool.Key]int32 {
	out := map[pool.Key]int32{}
	for i := range tjs {
		tj := &tjs[i]
		if !dispatched(tj.Status.Phase) || tj.Status.Hardware == "" {
			continue
		}
		if tp, ok := profiles[tj.Spec.ProfileRef]; ok {
			out[poolKeyFor(tp, tj.Status.Hardware)]++
		}
	}
	return out
}

// pools applies every pool this pass needs, from the work admission has
// dispatched: stored is the pool Jobs as listed at the start of the pass,
// and dispatched the Queued and Running jobs per pool after it.
//
// Per pool, [pool.Next] decides and one apply (or delete) carries it out:
// create or resume with the work that arrived, raise parallelism, suspend at
// zero, suspend to drain a drifted pool, reshape it once stopped, or delete
// it for recreation -- a Failed pool after a backoff. Every apply is
// [pool.Render]'s complete declaration under squasharr-pool.
func (r *Reconciler) pools(ctx context.Context, stored map[string]*batchv1.Job,
	profiles map[string]*transcodev1alpha1.TranscodeProfile, dispatched map[pool.Key]int32,
) error {
	log := logging.FromContext(ctx)
	keys := maps.Clone(dispatched)
	if keys == nil {
		keys = map[pool.Key]int32{}
	}
	for _, j := range stored { // a pool with no dispatched work still needs its suspend
		if uid := ownerProfileUID(j); uid != "" {
			k := pool.Key{
				Profile: j.Labels[pool.LabelProfile], ProfileUID: uid,
				Class: transcodev1alpha1.Hardware(j.Labels[pool.LabelHardware]),
			}
			keys[k] += 0
		}
	}

	var errs []error
	for k, n := range keys {
		tp, ok := profiles[k.Profile]
		if !ok || tp.UID != k.ProfileUID {
			continue // a deleted or replaced profile's pool goes with it: garbage collection, by owner reference
		}
		name := pool.Name(k)
		cur := stored[name]
		if cur == nil {
			delete(r.recreate, name)
		} else if uid := ownerProfileUID(cur); uid != k.ProfileUID {
			log.InfoContext(ctx, "squasharr: a Job another owner holds has this pool's name; holding its work until it goes",
				"pool", name, "ownerUID", uid)
			continue
		}
		want := pool.Want(tp, k.Class, r.Pool)
		d, act := pool.Next(cur, n, r.drift(cur, want))
		switch act {
		case pool.ActionDelete:
			errs = append(errs, r.deletePool(ctx, tp, cur))
		case pool.ActionApply:
			if cur == nil && !r.poolBackoffOver(name) {
				continue // a Failed pool waits out its backoff; its tasks stay queued
			}
			errs = append(errs, r.applyPool(ctx, k, tp, want, d, cur, n))
		}
	}
	return errors.Join(errs...)
}

// applyPool renders and applies one pool's desired shape d.
func (r *Reconciler) applyPool(ctx context.Context, k pool.Key, tp *transcodev1alpha1.TranscodeProfile,
	want pool.Spec, d pool.Desired, cur *batchv1.Job, n int32,
) error {
	log := logging.FromContext(ctx)
	name := pool.Name(k)
	ac, err := pool.Render(k, tp, want, d, cur, r.Pool)
	if errors.Is(err, pool.ErrNoAppliedSpec) {
		// Ruling R15: the Job lost its applied-template annotation, so it
		// reads as immutable drift and Next asks to suspend it to drain --
		// which Render cannot express without the template it was applied
		// with. Next asks that only of a running pool with nothing
		// dispatched to it, so no work is lost: recreate it now rather than
		// wedge it.
		return r.deletePool(ctx, tp, cur)
	}
	if err != nil {
		return fmt.Errorf("transcodejob: render pool %s: %w", name, err)
	}
	_, err = k8s.Apply(ctx, r.Client, k8s.ManagerSquasharrPool, ac)
	if cur != nil && pool.IsSchedulingImmutable(err) {
		// The gang minCount every apply carries cannot be added to a Job
		// created before WorkloadWithJob was enabled (spec §7). That is
		// immutable drift, and no apply -- not even a suspend -- is
		// accepted on this Job again. With nothing dispatched to it, or
		// nothing running, it is recreated now; otherwise it drains first,
		// holding new work, and is deleted once its work is done.
		if n == 0 || pool.Mutable(cur) {
			return r.deletePool(ctx, tp, cur)
		}
		r.recreate[name] = true
		log.InfoContext(ctx, "squasharr: the pool predates gang scheduling; draining it for recreation", "pool", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("transcodejob: apply pool %s: %w", name, err)
	}
	log.DebugContext(ctx, "squasharr: applied pool", "pool", name, "parallelism", d.Parallelism, "suspend", d.Suspend,
		"dispatched", n, "created", cur == nil)
	return nil
}

// deletePool deletes a pool Job so the next pass with work recreates it:
// with background propagation, so its pods go after it, and conditional on
// its UID, so a pool recreated since the pass listed it survives. A Failed
// pool also gets a Warning Event on its TranscodeProfile and a backoff
// before it is recreated (spec §7).
func (r *Reconciler) deletePool(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile, j *batchv1.Job) error {
	log := logging.FromContext(ctx)
	err := r.Client.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground),
		client.Preconditions{UID: ptr.To(j.UID)})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("transcodejob: delete pool %s: %w", j.Name, err)
	}
	delete(r.recreate, j.Name)
	if !pool.Failed(j) {
		log.InfoContext(ctx, "squasharr: deleted pool for recreation", "pool", j.Name)
		return nil
	}
	delay := r.backOffPool(j.Name)
	why := "it failed"
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			why = fmt.Sprintf("%s: %s", c.Reason, c.Message)
		}
	}
	log.WarnContext(ctx, "squasharr: pool failed; recreating it after a backoff", "pool", j.Name, "reason", why, "backoff", delay)
	if r.Recorder != nil {
		r.Recorder.Eventf(tp, nil, corev1.EventTypeWarning, ReasonPoolFailed, "RecreatePool",
			"pool Job %s failed (%s); it is recreated in %s", j.Name, why, delay)
	}
	return nil
}

// backOffPool starts name's recreation backoff and returns its length: the
// minimum, or double the last one when this failure follows the end of that
// wait within poolBackoffMax.
func (r *Reconciler) backOffPool(name string) time.Duration {
	now := r.now().Time
	delay := poolBackoffMin
	if prev, ok := r.poolBackoff[name]; ok && now.Sub(prev.until) < poolBackoffMax {
		delay = min(prev.delay*2, poolBackoffMax)
	}
	r.poolBackoff[name] = poolBackoff{until: now.Add(delay), delay: delay}
	return delay
}

// poolBackoffOver reports whether name may be created: it never failed, or
// its backoff has passed.
func (r *Reconciler) poolBackoffOver(name string) bool {
	b, ok := r.poolBackoff[name]
	return !ok || !r.now().Time.Before(b.until)
}

// isPoolJob passes the Jobs squasharr's pools are.
func isPoolJob(o client.Object) bool {
	l := o.GetLabels()
	_, ok := l[pool.LabelProfile]
	return ok && l[pool.LabelManagedBy] == pool.ManagedByValue
}

// poolSignal is what an admission pass acts on in a pool Job: its suspend,
// whether its pods are running and started (a template may change only once
// both are clear), whether it failed, and its template hash.
func poolSignal(o client.Object) string {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return ""
	}
	return fmt.Sprintf("suspend=%t active=%d started=%t failed=%t hash=%s",
		ptr.Deref(j.Spec.Suspend, false), j.Status.Active, j.Status.StartTime != nil, pool.Failed(j),
		j.Labels[pool.LabelTemplateHash])
}

// poolJobPredicate passes a pool Job's creation, deletion and every change
// to its [poolSignal], and nothing about any other Job.
func poolJobPredicate() predicate.Predicate {
	return k8s.And(predicate.NewPredicateFuncs(isPoolJob), k8s.StatusFieldChanged(poolSignal))
}
