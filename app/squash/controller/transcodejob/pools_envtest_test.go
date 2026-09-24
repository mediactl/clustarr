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

package transcodejob_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// admitPass runs one admission pass, as the results consumer's wake and the
// pool Job watch enqueue it. A terminal job's own reconcile runs none.
func admitPass(t *testing.T, r *transcodejob.Reconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: transcodejob.AdmissionRequestForTest})
	require.NoError(t, err)
}

func poolKey(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) types.NamespacedName {
	return types.NamespacedName{Namespace: "default", Name: pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class})}
}

func getPool(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) *batchv1.Job {
	t.Helper()
	var j batchv1.Job
	require.NoError(t, c.Get(context.Background(), poolKey(tp, class), &j))
	return &j
}

func poolGone(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) bool {
	t.Helper()
	err := c.Get(context.Background(), poolKey(tp, class), &batchv1.Job{})
	if apierrors.IsNotFound(err) {
		return true
	}
	require.NoError(t, err)
	return false
}

// setPoolRunning is the Job controller starting a pool's pods.
func setPoolRunning(t *testing.T, c client.Client, j *batchv1.Job, active int32) {
	t.Helper()
	j.Status.StartTime, j.Status.Active = &metav1.Time{Time: time.Now()}, active
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

// setPoolStopped is the Job controller acting on a suspend: the pods are
// gone and startTime is cleared, which is when a template may change.
func setPoolStopped(t *testing.T, c client.Client, j *batchv1.Job) {
	t.Helper()
	j.Status.StartTime, j.Status.Active = nil, 0
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

// setPoolFailed is the Job controller giving up on a pool: backoffLimit
// spent on worker-level pod failures.
func setPoolFailed(t *testing.T, c client.Client, j *batchv1.Job) {
	t.Helper()
	now := metav1.Now()
	if j.Status.StartTime == nil {
		j.Status.StartTime = &now
	}
	j.Status.Active, j.Status.Failed = 0, pool.BackoffLimit+1
	j.Status.Conditions = []batchv1.JobCondition{
		{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded,
			Message: "Job has reached the specified backoff limit", LastProbeTime: now, LastTransitionTime: now,
		},
		{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded,
			Message: "Job has reached the specified backoff limit", LastProbeTime: now, LastTransitionTime: now,
		},
	}
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

// succeeded is a worker's final report of a transcode that worked.
func succeeded(t *testing.T, tj *transcodev1alpha1.TranscodeJob, attempt int32) task.StatusEvent {
	t.Helper()
	ev := finished(attempt, task.OutcomeSucceeded, "", "")
	ev.Result = &transcodev1alpha1.Result{OutputPath: tj.Spec.SourcePath, OutputSizeBytes: 1 << 30, OutputToSourcePercent: 50}
	return ev
}

// runToSuccess is a pool worker claiming tj's current attempt and
// finishing it.
func runToSuccess(t *testing.T, r *transcodejob.Reconciler, c client.Client, ns, name string) {
	t.Helper()
	tj := getTJ(t, c, ns, name)
	require.NoError(t, deliver(t, r, tj, claimed(tj.Status.Attempts, "pool-"+name)))
	require.NoError(t, deliver(t, r, tj, succeeded(t, tj, tj.Status.Attempts)))
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, getTJ(t, c, ns, name).Status.Phase)
}

// editProfile applies mutate to the live profile, as an operator's edit.
func editProfile(t *testing.T, c client.Client, name string, mutate func(*transcodev1alpha1.TranscodeProfile)) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	ctx := context.Background()
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &tp))
	mutate(&tp)
	require.NoError(t, c.Update(ctx, &tp))
	return &tp
}

func cpuLimit(j *batchv1.Job) string {
	return j.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()
}

// TestPoolFollowsDispatch: the admission pass sizes a (profile, class) pool
// Job to the work dispatched to it -- created with the first task, raised
// with the second, owned by its profile -- never shrinks it below its
// parallelism while work remains, and suspends it once none does, without
// ever sending parallelism 0.
func TestPoolFollowsDispatch(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-pool"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	for _, name := range []string{"a", "b"} {
		newMediaFile(t, c, ns, name, "p"+name, ptr.To(h264Probe()))
		newTJ(t, c, ns, name+"-hevc", name, "hevc", "p"+name, nil)
	}
	r := newReconciler(t, c, map[string]int32{"cpu": 2})

	reconcileTJ(t, r, ns, "a-hevc")
	j := getPool(t, c, tp, "cpu")
	assert.EqualValues(t, 1, *j.Spec.Parallelism)
	assert.False(t, *j.Spec.Suspend)
	require.Len(t, j.OwnerReferences, 1)
	assert.Equal(t, tp.UID, j.OwnerReferences[0].UID)
	assert.Equal(t, "TranscodeProfile", j.OwnerReferences[0].Kind)
	assert.True(t, *j.OwnerReferences[0].Controller)
	assert.Equal(t, "transcoder:test", j.Spec.Template.Spec.Containers[0].Image)
	require.NotNil(t, getTJ(t, c, ns, "a-hevc").Status.JobRef)
	assert.Equal(t, j.Name, *getTJ(t, c, ns, "a-hevc").Status.JobRef, "jobRef names the pool the task went to")

	reconcileTJ(t, r, ns, "b-hevc")
	j = getPool(t, c, tp, "cpu")
	assert.EqualValues(t, 2, *j.Spec.Parallelism, "a second dispatched task raises the pool")
	assert.False(t, *j.Spec.Suspend)

	runToSuccess(t, r, c, ns, "a-hevc")
	admitPass(t, r)
	j = getPool(t, c, tp, "cpu")
	assert.EqualValues(t, 2, *j.Spec.Parallelism, "v1 shrinks only to zero: an idle worker waits for the queue")
	assert.False(t, *j.Spec.Suspend, "one task is still dispatched to the pool")

	runToSuccess(t, r, c, ns, "b-hevc")
	admitPass(t, r)
	j = getPool(t, c, tp, "cpu")
	assert.True(t, *j.Spec.Suspend, "no dispatched work: the pool suspends to zero")
	assert.EqualValues(t, 2, *j.Spec.Parallelism, "parallelism is never zeroed")

	rv := j.ResourceVersion
	admitPass(t, r)
	assert.Equal(t, rv, getPool(t, c, tp, "cpu").ResourceVersion, "an idle suspended pool is left alone")

	managers := map[string]bool{}
	for _, mf := range j.ManagedFields {
		if mf.Operation == metav1.ManagedFieldsOperationApply {
			managers[mf.Manager] = true
		}
	}
	assert.Equal(t, map[string]bool{string(k8s.ManagerSquasharrPool): true}, managers, "squasharr-pool is the pool's one applier")
}

// TestAQueuedTaskKeepsItsClassPoolAwake is the Task 10 carry (R16): the
// class a task was dispatched to is authoritative once published, so the
// pool that is counted for a Queued or Running job is the one its
// status.hardware names -- never the class admission would choose now. A job
// planned for libx265 (a cpu class) whose worker reported from the nvidia
// pool keeps the nvidia pool running, and the cpu pool, with nothing
// dispatched to it, is suspended.
func TestAQueuedTaskKeepsItsClassPoolAwake(t *testing.T) {
	f := newDispatched(t, "tj-carry", map[string]int32{"cpu": 1})
	require.NotNil(t, f.tj.Status.Plan)
	require.Equal(t, "libx265", f.tj.Status.Plan.Encoder, "classFor would choose cpu")
	require.False(t, *getPool(t, f.c, f.tp, "cpu").Spec.Suspend)

	// The task went to the nvidia pool: its worker's claim says so (R16).
	ev := claimed(1, "pool-gpu")
	ev.Class = transcodev1alpha1.HardwareNVIDIA
	require.NoError(t, deliver(t, f.r, f.tj, ev))
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, f.get(t).Status.Hardware)

	admitPass(t, f.r)
	gpu := getPool(t, f.c, f.tp, "nvidia")
	assert.False(t, *gpu.Spec.Suspend, "the pool status.hardware names is never suspended under its task")
	assert.GreaterOrEqual(t, *gpu.Spec.Parallelism, int32(1))
	assert.Equal(t, "transcoder-cuda:test", gpu.Spec.Template.Spec.Containers[0].Image)
	assert.True(t, *getPool(t, f.c, f.tp, "cpu").Spec.Suspend, "nothing is dispatched to the cpu pool any more")
}

// TestDrainingPoolHoldsNewWorkThenReshapes is spec §7's mutable drift. A
// profile edited while its pool runs holds new work for that pool (the pool
// must drain, and a new task would run under the old template); the pool
// finishes what it has and suspends; and once the Job controller has cleared
// startTime, one pass dispatches the held job and reshapes and resumes the
// pool in one apply.
func TestDrainingPoolHoldsNewWorkThenReshapes(t *testing.T) {
	f := newDispatched(t, "tj-drain", map[string]int32{"cpu": 2})
	require.Equal(t, []int32{1}, attemptsOf(takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second)))
	j := getPool(t, f.c, f.tp, "cpu")
	require.Equal(t, "8", cpuLimit(j), "setup: the CRD default floor")
	setPoolRunning(t, f.c, j, 1)
	require.NoError(t, deliver(t, f.r, f.tj, claimed(1, "pool-a")))

	tp := editProfile(t, f.c, "hevc", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}
	})
	require.Equal(t, pool.DriftReshape, pool.Classify(getPool(t, f.c, f.tp, "cpu"), pool.Want(tp, "cpu", f.r.Pool)))

	newMediaFile(t, f.c, f.ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, f.c, f.ns, "b-hevc", "b", "hevc", "pb", nil)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	b := getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, b.Status.Phase, "a free slot, but the pool is draining")
	assert.Contains(t, b.Status.Message, "drain")
	assert.Contains(t, b.Status.Message, j.Name)
	assert.Zero(t, b.Status.Attempts)
	assert.Empty(t, takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 300*time.Millisecond), "nothing is published to a draining pool")
	j = getPool(t, f.c, f.tp, "cpu")
	assert.False(t, *j.Spec.Suspend, "the pool keeps its dispatched work")
	assert.Equal(t, "8", cpuLimit(j), "a running pool keeps its template")

	runToSuccess(t, f.r, f.c, f.ns, f.tj.Name)
	admitPass(t, f.r)
	j = getPool(t, f.c, f.tp, "cpu")
	assert.True(t, *j.Spec.Suspend, "drained: the pool suspends")
	assert.Equal(t, "8", cpuLimit(j), "the template waits for startTime to clear")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, f.c, f.ns, "b-hevc").Status.Phase)

	setPoolStopped(t, f.c, j)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	b = getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, b.Status.Phase, "message: %s", b.Status.Message)
	assert.EqualValues(t, 1, b.Status.Attempts)
	assert.Equal(t, []int32{1}, attemptsOf(takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second)))
	j = getPool(t, f.c, f.tp, "cpu")
	assert.False(t, *j.Spec.Suspend, "resumed")
	assert.EqualValues(t, 1, *j.Spec.Parallelism)
	assert.Equal(t, "2", cpuLimit(j), "reshaped in the same apply")
	assert.Equal(t, pool.DriftNone, pool.Classify(j, pool.Want(tp, "cpu", f.r.Pool)))
}

// TestImageChangeRecreatesTheIdlePool is spec §7's immutable drift on an
// idle pool: a new worker image cannot be applied to a Job, so the
// suspended, stopped pool is deleted, and the next pass with work creates it
// again with the new image.
func TestImageChangeRecreatesTheIdlePool(t *testing.T) {
	f := newDispatched(t, "tj-image", map[string]int32{"cpu": 1})
	runToSuccess(t, f.r, f.c, f.ns, f.tj.Name)
	admitPass(t, f.r)
	old := getPool(t, f.c, f.tp, "cpu")
	require.True(t, *old.Spec.Suspend)
	require.Nil(t, old.Status.StartTime)
	require.Equal(t, "transcoder:test", old.Spec.Template.Spec.Containers[0].Image)

	f.r.Pool.Image = "transcoder:new"
	admitPass(t, f.r)
	assert.True(t, poolGone(t, f.c, f.tp, "cpu"), "an idle pool with immutable drift is deleted")

	newMediaFile(t, f.c, f.ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, f.c, f.ns, "b-hevc", "b", "hevc", "pb", nil)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, f.c, f.ns, "b-hevc").Status.Phase)
	j := getPool(t, f.c, f.tp, "cpu")
	assert.NotEqual(t, old.UID, j.UID)
	assert.Equal(t, "transcoder:new", j.Spec.Template.Spec.Containers[0].Image)
	assert.False(t, *j.Spec.Suspend)
	assert.EqualValues(t, 1, *j.Spec.Parallelism)
}

// recordedEvent is one Event a recorder was asked for.
type recordedEvent struct {
	regarding runtime.Object
	eventType string
	reason    string
	note      string
}

// capturingRecorder keeps every Event with the object it is about, which
// FakeRecorder does not.
type capturingRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (r *capturingRecorder) Eventf(regarding, _ runtime.Object, eventType, reason, _, note string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{regarding: regarding, eventType: eventType, reason: reason, note: fmt.Sprintf(note, args...)})
}

func (r *capturingRecorder) take(reason string) []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out, rest []recordedEvent
	for _, e := range r.events {
		if e.reason == reason {
			out = append(out, e)
		} else {
			rest = append(rest, e)
		}
	}
	r.events = rest
	return out
}

// TestFailedPoolIsRecreatedWithBackoff is spec §7's Failed pool: it is
// deleted with a Warning Event on its TranscodeProfile, recreated only once
// its backoff has passed -- though its task is still queued -- and a second
// failure soon after doubles the wait. While it is failed or backing off,
// admission sends it no new work (R20): a free slot of its class is not
// spent on a task with no pool to run it.
func TestFailedPoolIsRecreatedWithBackoff(t *testing.T) {
	f := newDispatched(t, "tj-failed", map[string]int32{"cpu": 2})
	rec := &capturingRecorder{}
	f.r.Recorder = rec
	now := time.Now()
	f.r.Now = func() time.Time { return now }

	first := getPool(t, f.c, f.tp, "cpu")
	setPoolFailed(t, f.c, first)
	newMediaFile(t, f.c, f.ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, f.c, f.ns, "b-hevc", "b", "hevc", "pb", nil)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	assert.True(t, poolGone(t, f.c, f.tp, "cpu"), "a Failed pool is deleted")
	b := getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, b.Status.Phase, "a free slot, but the pool failed")
	assert.Contains(t, b.Status.Message, "waiting for pool "+first.Name+" to recover: it failed at")
	warned := rec.take(transcodejob.ReasonPoolFailed)
	require.Len(t, warned, 1)
	assert.Equal(t, corev1.EventTypeWarning, warned[0].eventType)
	on, ok := warned[0].regarding.(*transcodev1alpha1.TranscodeProfile)
	require.True(t, ok, "the Event is on the TranscodeProfile, not %T", warned[0].regarding)
	assert.Equal(t, f.tp.UID, on.UID)
	assert.Contains(t, warned[0].note, first.Name)
	assert.Contains(t, warned[0].note, "1m0s")

	now = now.Add(30 * time.Second)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	assert.True(t, poolGone(t, f.c, f.tp, "cpu"), "inside the backoff nothing is created")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, f.get(t).Status.Phase, "the task stays queued")
	b = getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, b.Status.Phase, "nothing new goes to a pool in backoff")
	assert.Zero(t, b.Status.Attempts)
	assert.Contains(t, b.Status.Message, "is recreated at "+now.Add(30*time.Second).UTC().Format(time.RFC3339))

	now = now.Add(31 * time.Second)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	second := getPool(t, f.c, f.tp, "cpu")
	assert.NotEqual(t, first.UID, second.UID)
	assert.False(t, *second.Spec.Suspend)
	assert.EqualValues(t, 2, *second.Spec.Parallelism, "the queued task and the one held for it")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, f.c, f.ns, "b-hevc").Status.Phase)

	// It fails again soon after: the wait doubles.
	setPoolFailed(t, f.c, second)
	admitPass(t, f.r)
	require.True(t, poolGone(t, f.c, f.tp, "cpu"))
	warned = rec.take(transcodejob.ReasonPoolFailed)
	require.Len(t, warned, 1)
	assert.Contains(t, warned[0].note, "2m0s")
	now = now.Add(90 * time.Second)
	admitPass(t, f.r)
	assert.True(t, poolGone(t, f.c, f.tp, "cpu"), "still inside the doubled backoff")
	now = now.Add(31 * time.Second)
	admitPass(t, f.r)
	assert.False(t, poolGone(t, f.c, f.tp, "cpu"))
}

// TestAPoolWithoutItsAppliedTemplateIsReplaced is ruling R15: a running
// pool Job whose applied-template annotation is gone (an operator's edit)
// cannot be rendered, so it could never be suspended to drain. It holds new
// work while its task runs, and once that is done it is deleted -- the pass
// does not fail, and the pool does not wedge -- and the held job gets a
// fresh pool.
func TestAPoolWithoutItsAppliedTemplateIsReplaced(t *testing.T) {
	f := newDispatched(t, "tj-r15", map[string]int32{"cpu": 2})
	ctx := context.Background()
	j := getPool(t, f.c, f.tp, "cpu")
	setPoolRunning(t, f.c, j, 1)
	require.NoError(t, deliver(t, f.r, f.tj, claimed(1, "pool-a")))

	j = getPool(t, f.c, f.tp, "cpu")
	patch := client.MergeFrom(j.DeepCopy())
	delete(j.Annotations, pool.AnnotationAppliedTemplate)
	require.NoError(t, f.c.Patch(ctx, j, patch))
	require.NotContains(t, getPool(t, f.c, f.tp, "cpu").Annotations, pool.AnnotationAppliedTemplate)

	newMediaFile(t, f.c, f.ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, f.c, f.ns, "b-hevc", "b", "hevc", "pb", nil)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	b := getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, b.Status.Phase)
	assert.Contains(t, b.Status.Message, "drain")

	runToSuccess(t, f.r, f.c, f.ns, f.tj.Name)
	admitPass(t, f.r) // must not fail: a render error here would wedge the pool
	assert.True(t, poolGone(t, f.c, f.tp, "cpu"), "replaced, not wedged")

	reconcileTJ(t, f.r, f.ns, "b-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, f.c, f.ns, "b-hevc").Status.Phase)
	fresh := getPool(t, f.c, f.tp, "cpu")
	assert.NotEqual(t, j.UID, fresh.UID)
	assert.Contains(t, fresh.Annotations, pool.AnnotationAppliedTemplate)
	assert.False(t, *fresh.Spec.Suspend)
}

// startGatedEnv is startEnv with WorkloadWithJob on, so the apiserver keeps
// a pool's gang minCount and refuses one added to a Job created without it.
func startGatedEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	env.ControlPlane.GetAPIServer().Configure().
		Append("feature-gates", "WorkloadWithJob=true,GenericWorkload=true").
		Append("runtime-config", "scheduling.k8s.io/v1alpha3=true")
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// TestAGateEnabledLaterDrainsAndRecreatesThePool is spec §7's gate change:
// a pool created before WorkloadWithJob has no .spec.scheduling, and every
// apply now carries minCount, which the apiserver refuses on it. That is
// immutable drift: the busy pool keeps its work and holds new work, and once
// drained it is deleted -- it could not even be suspended -- and recreated
// with the gang.
func TestAGateEnabledLaterDrainsAndRecreatesThePool(t *testing.T) {
	c := startGatedEnv(t)
	ctx := context.Background()
	const ns = "tj-gate"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 3})

	// The pool as squasharr-pool made it before the gate: no .spec.scheduling.
	k := pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}
	ac, err := pool.Render(k, tp, pool.Want(tp, "cpu", r.Pool), pool.Desired{Parallelism: 1}, nil, r.Pool)
	require.NoError(t, err)
	ac.Spec.Scheduling = nil
	_, err = k8s.Apply(ctx, c, k8s.ManagerSquasharrPool, ac)
	require.NoError(t, err)
	old := getPool(t, c, tp, "cpu")
	require.Nil(t, old.Spec.Scheduling)
	setPoolRunning(t, c, old, 1)

	for _, name := range []string{"a", "b", "c"} {
		newMediaFile(t, c, ns, name, "p"+name, ptr.To(h264Probe()))
		newTJ(t, c, ns, name+"-hevc", name, "hevc", "p"+name, nil)
	}
	reconcileTJ(t, r, ns, "a-hevc") // one task, parallelism 1: nothing to apply
	reconcileTJ(t, r, ns, "b-hevc") // two: the raise is refused, which marks the pool for recreation
	for _, name := range []string{"a-hevc", "b-hevc"} {
		require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, name).Status.Phase)
	}
	assert.Equal(t, old.UID, getPool(t, c, tp, "cpu").UID, "a busy pool is not deleted under its work")

	reconcileTJ(t, r, ns, "c-hevc")
	held := getTJ(t, c, ns, "c-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, held.Status.Phase, "a slot is free, but the pool is draining")
	assert.Contains(t, held.Status.Message, "drain")

	runToSuccess(t, r, c, ns, "a-hevc")
	runToSuccess(t, r, c, ns, "b-hevc")
	admitPass(t, r)
	assert.True(t, poolGone(t, c, tp, "cpu"), "drained: deleted, since not even a suspend can be applied to it")

	reconcileTJ(t, r, ns, "c-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "c-hevc").Status.Phase)
	fresh := getPool(t, c, tp, "cpu")
	assert.NotEqual(t, old.UID, fresh.UID)
	require.NotNil(t, fresh.Spec.Scheduling)
	assert.EqualValues(t, 1, *fresh.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	assert.False(t, *fresh.Spec.Suspend)
}

// TestAPoolJobChangeWakesAdmission runs the real manager, its Job cache
// restricted as squasharr's is: a pool Job deleted by hand while its task is
// queued is recreated by the pass the Job watch wakes, not by the job's own
// requeue a minute later.
func TestAPoolJobChangeWakesAdmission(t *testing.T) {
	cfg, c := startEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		// As squasharr's manager caches Jobs (R21): the pool Jobs alone.
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&batchv1.Job{}: transcodejob.PoolJobCache("default"),
		}},
	})
	require.NoError(t, err)
	r := newReconciler(t, mgr.GetClient(), map[string]int32{"cpu": 1})
	r.Reader = mgr.GetAPIReader()
	require.NoError(t, r.SetupWithManager(mgr))
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	const ns = "tj-poolwatch"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)

	var first *batchv1.Job
	require.Eventually(t, func() bool {
		var j batchv1.Job
		if c.Get(ctx, poolKey(tp, "cpu"), &j) != nil {
			return false
		}
		first = &j
		return true
	}, 20*time.Second, 100*time.Millisecond, "dispatch creates the pool")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "a-hevc").Status.Phase)

	require.NoError(t, c.Delete(ctx, first, client.PropagationPolicy(metav1.DeletePropagationBackground)))
	require.Eventually(t, func() bool {
		var j batchv1.Job
		return c.Get(ctx, poolKey(tp, "cpu"), &j) == nil && j.UID != first.UID
	}, 10*time.Second, 100*time.Millisecond, "the Job watch wakes admission, which recreates the pool for its queued task")
}

// TestALongProfileNameGetsAWorkingPool is R19: a TranscodeProfile name may be
// 253 characters, a label value 63. The pool's profile label is label-safe
// and the pool is found by the name its profile's UID derives, never by
// reading that label back -- so a 100-character profile gets a pool that is
// created, sized and suspended like any other.
func TestALongProfileNameGetsAWorkingPool(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-longname"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	name := strings.Repeat("h", 50) + "." + strings.Repeat("e", 49)
	require.Len(t, name, 100)
	tp := newProfile(t, c, name, "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-long", "a", name, "pa", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})

	reconcileTJ(t, r, ns, "a-long")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "a-long").Status.Phase)
	j := getPool(t, c, tp, "cpu")
	assert.LessOrEqual(t, len(j.Name), 63)
	for key, v := range j.Labels {
		assert.Empty(t, validation.IsValidLabelValue(v), "label %s=%q", key, v)
	}
	assert.Equal(t, pool.ProfileLabelValue(name), j.Labels[pool.LabelProfile])
	assert.False(t, *j.Spec.Suspend)

	runToSuccess(t, r, c, ns, "a-long")
	admitPass(t, r)
	assert.True(t, *getPool(t, c, tp, "cpu").Spec.Suspend, "found by its name, the idle pool suspends")
}

// TestAPoolJobItDoesNotOwnHoldsItsWork is R22: a Job at a pool's name whose
// controller owner reference is not the profile -- only an operator's edit
// of its ownerReferences makes one, since pool.Name hashes the profile's
// UID -- is neither applied to nor deleted, and the work for it is held
// with a message that says what to do.
func TestAPoolJobItDoesNotOwnHoldsItsWork(t *testing.T) {
	f := newDispatched(t, "tj-foreign", map[string]int32{"cpu": 2})
	ctx := context.Background()
	j := getPool(t, f.c, f.tp, "cpu")
	patch := client.MergeFrom(j.DeepCopy())
	j.OwnerReferences = nil
	require.NoError(t, f.c.Patch(ctx, j, patch))
	rv := getPool(t, f.c, f.tp, "cpu").ResourceVersion

	newMediaFile(t, f.c, f.ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, f.c, f.ns, "b-hevc", "b", "hevc", "pb", nil)
	reconcileTJ(t, f.r, f.ns, "b-hevc")
	b := getTJ(t, f.c, f.ns, "b-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, b.Status.Phase)
	assert.Contains(t, b.Status.Message, j.Name)
	assert.Contains(t, b.Status.Message, "owner reference")
	assert.Contains(t, b.Status.Message, "delete the Job")
	assert.Equal(t, rv, getPool(t, f.c, f.tp, "cpu").ResourceVersion, "a Job squasharr does not own is left alone")
}
