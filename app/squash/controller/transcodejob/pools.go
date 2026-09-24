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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ReasonPoolFailed is the Warning Event reason on a TranscodeProfile whose
// pool Job failed and is recreated.
const ReasonPoolFailed = "PoolFailed"

// ReasonPoolUnschedulable is the Warning Event reason on a TranscodeProfile
// whose GPU pool cannot get a pod scheduled, and JobCreated=False's reason on
// each auto job taken back from that pool (spec §18.5).
const ReasonPoolUnschedulable = "PoolUnschedulable"

// A GPU pool with a pod unschedulable for longer than unschedulableAfter is
// marked unschedulable for unschedulableFor: no auto job is sent to it, and
// its Queued auto jobs are taken back and sent to cpu (spec §18.5).
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

// heldPrefix starts the message of a Planned job admission holds for a
// draining pool, so a later pass can tell the message is its own.
const heldPrefix = "waiting for pool "

// poolBackoff is one pool's recreation backoff: no create before until, and
// delay is the wait that until was set with, so the next one can double it.
// It is kept in memory: a restart forgets it, which only shortens one wait.
type poolBackoff struct {
	failedAt time.Time
	until    time.Time
	delay    time.Duration
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

// poolClasses are the hardware classes a pool can be for: every class a
// task is dispatched to (auto is resolved before dispatch).
var poolClasses = []transcodev1alpha1.Hardware{
	transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
}

// storedPools is the key of every pool Job of a current profile: the
// profile's pool name for a class, found among stored. A pool is identified
// by the name its profile's UID and class derive, never by reading its
// labels back: the profile label is only label-safe, not the name
// (pool.ProfileLabelValue). A Job of a deleted profile is none of these; it
// goes with its owner, by garbage collection.
func storedPools(stored map[string]*batchv1.Job, profiles map[string]*transcodev1alpha1.TranscodeProfile) map[pool.Key]*batchv1.Job {
	out := map[pool.Key]*batchv1.Job{}
	for _, tp := range profiles {
		for _, class := range poolClasses {
			k := poolKeyFor(tp, class)
			if j, ok := stored[pool.Name(k)]; ok {
				out[k] = j
			}
		}
	}
	return out
}

// holding is the set of pools admission dispatches nothing new to in this
// pass, each with the message its held jobs carry. It is judged before
// admission, from the pools as they are now, so the pass that first sees a
// change already holds. A pool is held while:
//
//   - its template drifted and its pods may still run (spec §7,
//     "draining"): it must finish its dispatched work and suspend before the
//     change can land, and a new task would both keep it busy and run under
//     the old template. A drifted pool that is suspended and stopped
//     ([pool.Mutable]) is not held: the apply after dispatch reshapes and
//     resumes it in one, or it is deleted and recreated for its work.
//   - it failed, or waits out the backoff before it is recreated (R20):
//     without a pool, every task dispatched to it would take a slot of its
//     class and wait, so a failing profile could hold every slot for as long
//     as the backoff lasts. Its tasks already dispatched stay queued.
//   - its name is taken by a Job whose controller owner is not this profile
//     (R22). pool.Name hashes the profile's UID, so no other incarnation of
//     the profile produces that name: only an edit of the Job's
//     ownerReferences does, and squasharr neither adopts nor deletes a Job
//     it does not own. The message tells the operator to delete it.
//
// A drain lasts as long as the pool's slowest dispatched job.
func (r *Reconciler) holding(stored map[string]*batchv1.Job, profiles map[string]*transcodev1alpha1.TranscodeProfile) map[pool.Key]string {
	held := map[pool.Key]string{}
	for _, tp := range profiles {
		for _, class := range poolClasses {
			k := poolKeyFor(tp, class)
			name := pool.Name(k)
			j, ok := stored[name]
			switch {
			case !ok:
				if b, ok := r.poolBackoff[name]; ok && !r.poolBackoffOver(name) {
					held[k] = fmt.Sprintf("%s%s to recover: it failed at %s and is recreated at %s",
						heldPrefix, name, b.failedAt.UTC().Format(time.RFC3339), b.until.UTC().Format(time.RFC3339))
				}
			case ownerProfileUID(j) != tp.UID:
				held[k] = fmt.Sprintf("%s%s: that Job's controller owner reference is not TranscodeProfile %s (uid %s), "+
					"so squasharr will not touch it; delete the Job and the pool is recreated", heldPrefix, name, tp.Name, tp.UID)
			case pool.Failed(j):
				held[k] = fmt.Sprintf("%s%s to recover: it failed at %s", heldPrefix, name, failedAt(j, r.now().Time).UTC().Format(time.RFC3339))
			case pool.Mutable(j):
			case r.drift(j, pool.Want(tp, class, r.Pool)) != pool.DriftNone:
				held[k] = heldPrefix + name + " to drain before its profile change applies"
			}
		}
	}
	return held
}

// rerouteUnschedulable marks every GPU pool with a pod that has waited
// unschedulable -- PodScheduled=False, reason Unschedulable -- for longer
// than unschedulableAfter, and takes its Queued auto jobs back (spec §18.5).
// It returns how many jobs it sent back to Planned.
//
// A marked pool gets no auto job until the mark lapses, unschedulableFor
// after it was set (ChooseClass reads it through unschedulableFor). The mark
// is in memory: a restart forgets it, and the next pass that sees the pod
// still waiting marks the pool again -- within unschedulableAfter of the
// pod's own transition, since the pod carries when it began to wait. A
// Warning Event on the profile names the pool once per mark.
//
// A pool is looked at only while it runs (not suspended) and has fewer Ready
// pods than active ones: a pool whose every pod is Ready has none waiting for
// a node, and costs no pod list.
//
// Each auto job Queued on the pool -- dispatched, not yet claimed -- is
// withdrawn and returned to Planned with a fallbackReason naming the pool
// (reroute), which keeps it on cpu from then on. A pinned job is left where
// it is: a pinned class never falls back. The pool, with nothing dispatched
// to it any more, suspends in the same pass (pools).
func (r *Reconciler) rerouteUnschedulable(ctx context.Context, stored map[string]*batchv1.Job,
	profiles map[string]*transcodev1alpha1.TranscodeProfile, tjs []transcodev1alpha1.TranscodeJob,
) (int, error) {
	log := logging.FromContext(ctx)
	now := r.now().Time
	for k, until := range r.unschedulable {
		if !now.Before(until) {
			delete(r.unschedulable, k)
		}
	}
	var (
		errs     []error
		rerouted int
	)
	for k, j := range storedPools(stored, profiles) {
		if k.Class == transcodev1alpha1.HardwareCPU || ptr.Deref(j.Spec.Suspend, false) ||
			ownerProfileUID(j) != k.ProfileUID || j.Status.Active <= ptr.Deref(j.Status.Ready, 0) {
			continue
		}
		var pods corev1.PodList
		if err := r.reader().List(ctx, &pods, client.InNamespace(j.Namespace), client.MatchingLabels{
			batchv1.JobNameLabel: j.Name, batchv1.ControllerUidLabel: string(j.UID),
		}); err != nil {
			errs = append(errs, fmt.Errorf("transcodejob: list the pods of pool %s: %w", j.Name, err))
			continue
		}
		since := unschedulableSince(pods.Items)
		if since.IsZero() || now.Sub(since) <= unschedulableAfter {
			continue
		}
		tp := profiles[k.Profile]
		if until, marked := r.unschedulable[k]; !marked || !now.Before(until) {
			until = now.Add(unschedulableFor)
			r.unschedulable[k] = until
			log.WarnContext(ctx, "squasharr: a GPU pool cannot schedule its pods; its auto jobs go to cpu",
				"pool", j.Name, "unschedulableSince", since, "until", until)
			if r.Recorder != nil {
				r.Recorder.Eventf(tp, nil, corev1.EventTypeWarning, ReasonPoolUnschedulable, "Reroute",
					"pool Job %s has had a pod unschedulable since %s; its queued auto jobs go to cpu, and no auto job is sent to it until %s",
					j.Name, since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
			}
		}
		reason := fmt.Sprintf("GPU pool %s unschedulable for %dm", j.Name, int(unschedulableAfter.Minutes()))
		for i := range tjs {
			tj := &tjs[i]
			if tj.Spec.ProfileRef != tp.Name || tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseQueued ||
				tj.Status.Hardware != k.Class || !isAutoFor(tj, tp) ||
				k8s.IsDeleting(tj) || (tj.Spec.Suspend != nil && *tj.Spec.Suspend) {
				continue // a pinned job waits for its class; a deleting or paused one has its own path
			}
			ok, err := r.reroute(ctx, tj, reason)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if ok {
				rerouted++
				log.InfoContext(ctx, "squasharr: took a queued auto job back from an unschedulable GPU pool",
					"transcodeJob", client.ObjectKeyFromObject(tj).String(), "pool", j.Name, "attempt", tj.Status.Attempts)
			}
		}
	}
	return rerouted, errors.Join(errs...)
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

// reroute takes one Queued auto job back from a GPU pool that cannot
// schedule its pods (spec §18.5): withdraw its task, then, in one
// conditional write, return it to Planned with the fallbackReason that keeps
// it on cpu. The next dispatch is a new attempt, planned for cpu: once a
// task is published the class it went to is authoritative (ruling R16), so a
// job is moved by withdrawing and dispatching again, never by rewriting
// status.hardware. It reports whether it wrote.
//
// The write accepts the job in Queued OR Running, at the attempt it withdrew
// (R24). A pod of the pool that did schedule may claim the task between the
// listing and the withdrawal, and a claimed event may land before this
// write. withdraw's cancel marker, per job and attempt, stops that worker
// wherever the claim got to -- it reports a cancelled finished, which the
// next-step table ignores -- so a write that refused a Running job would
// leave it Running with no worker, for good. What it will not touch is a job
// that moved on to another attempt or out of dispatch meanwhile.
func (r *Reconciler) reroute(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, reason string) (bool, error) {
	if err := r.withdraw(ctx, tj); err != nil {
		return false, fmt.Errorf("transcodejob: withdraw %s from an unschedulable pool: %w", client.ObjectKeyFromObject(tj), err)
	}
	attempt := tj.Status.Attempts
	before, after, err := r.writeStatus(ctx, client.ObjectKeyFromObject(tj),
		func(live *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
			if live.UID != tj.UID || st.Attempts != attempt || !dispatched(st.Phase) {
				return false
			}
			st.Phase, st.WorkerPod, st.Progress, st.NextAttemptAt = transcodev1alpha1.TranscodeJobPhasePlanned, "", nil, nil
			st.FallbackReason = truncate(reason, maxFallbackReason)
			st.Message = truncate(fmt.Sprintf("attempt %d withdrawn: %s; requeued for cpu", attempt, reason), maxMessage)
			k8s.MarkFalse(live, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonPoolUnschedulable,
				"%s", st.Message)
			return true
		})
	if after != nil {
		r.afterWrite(ctx, after, &before)
	}
	return after != nil, client.IgnoreNotFound(err)
}

// failedAt is when j's Failed condition was set, or now when it says none.
func failedAt(j *batchv1.Job, now time.Time) time.Time {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return now
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
	for k := range storedPools(stored, profiles) { // a pool with no dispatched work still needs its suspend
		keys[k] += 0
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
			// An operator's edit of its ownerReferences (see holding): not
			// ours to apply to or delete. Its work is held, saying so.
			log.WarnContext(ctx, "squasharr: the pool's Job is not owned by its TranscodeProfile; leaving it alone until it is deleted",
				"pool", name, "profile", tp.Name, "ownerUID", uid)
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
	if cur != nil && pool.IsRecreateOnly(err) {
		// The Job was created with something the apiserver never lets
		// change: a pod failure policy an upgrade changed (I1's exit-137
		// rule), or no gang minCount -- which every apply carries and
		// cannot be added to a Job created before WorkloadWithJob was
		// enabled (spec §7). That is immutable drift, and no apply -- not
		// even a suspend -- is accepted on this Job again. With nothing
		// dispatched to it, or nothing running, it is recreated now;
		// otherwise it drains first, holding new work, and is deleted once
		// its work is done.
		if n == 0 || pool.Mutable(cur) {
			return r.deletePool(ctx, tp, cur)
		}
		r.recreate[name] = true
		log.InfoContext(ctx, "squasharr: the pool was created with a spec the apiserver will not change; draining it for recreation",
			"pool", name)
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
	delay := r.backOffPool(j.Name, failedAt(j, r.now().Time))
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

// backOffPool starts name's recreation backoff for a failure at failed and
// returns its length: the minimum, or double the last one when this failure
// follows the end of that wait within poolBackoffMax.
func (r *Reconciler) backOffPool(name string, failed time.Time) time.Duration {
	now := r.now().Time
	delay := poolBackoffMin
	if prev, ok := r.poolBackoff[name]; ok && now.Sub(prev.until) < poolBackoffMax {
		delay = min(prev.delay*2, poolBackoffMax)
	}
	r.poolBackoff[name] = poolBackoff{failedAt: failed, until: now.Add(delay), delay: delay}
	return delay
}

// poolBackoffOver reports whether name may be created: it never failed, or
// its backoff has passed.
func (r *Reconciler) poolBackoffOver(name string) bool {
	b, ok := r.poolBackoff[name]
	return !ok || !r.now().Time.Before(b.until)
}

// PoolJobCache is the one way squasharr's manager may cache batch/v1 Jobs
// (R21): the pool Jobs alone -- those in namespace, the pools' own, labelled
// managed-by=squasharr -- rather than every Job in the cluster. The pool Job
// watch (SetupWithManager) is what needs the informer; the pools are read
// through Reader (poolJobs), never the cache.
//
// A Job read through the cached client would therefore silently miss every
// other Job: a restricted cache answers NotFound for an object it does not
// hold, which is indistinguishable from a Job that does not exist. Read any
// other Job through the APIReader, or widen this first.
func PoolJobCache(namespace string) cache.ByObject {
	b := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{pool.LabelManagedBy: pool.ManagedByValue})}
	if namespace != "" {
		b.Namespaces = map[string]cache.Config{namespace: {}}
	}
	return b
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
