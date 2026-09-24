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
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
	"github.com/mediactl/clustarr/squasharr/controller/transcodejob"
	"github.com/mediactl/clustarr/squasharr/task"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// gpuNode creates a Ready node carrying label=true and gpus of res
// allocatable, as a GPU operator and its device plugin leave one.
func gpuNode(t *testing.T, c client.Client, name, label string, res corev1.ResourceName, gpus string) *corev1.Node {
	t.Helper()
	ctx := context.Background()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{label: "true"}}}
	require.NoError(t, c.Create(ctx, n))
	n.Status.Capacity = corev1.ResourceList{res: resource.MustParse(gpus), corev1.ResourceCPU: resource.MustParse("8")}
	n.Status.Allocatable = corev1.ResourceList{res: resource.MustParse(gpus), corev1.ResourceCPU: resource.MustParse("8")}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	require.NoError(t, c.Status().Update(ctx, n)) //nolint:forbidigo // simulating the kubelet
	return n
}

// nvidiaNode is a GPU node of the nvidia class under the default label.
func nvidiaNode(t *testing.T, c client.Client, name, gpus string) *corev1.Node {
	t.Helper()
	return gpuNode(t, c, name, pool.DefaultNodeLabelNVIDIA, pool.GPUResource[transcodev1alpha1.HardwareNVIDIA], gpus)
}

// setNodeReady is the kubelet reporting n Ready or not.
func setNodeReady(t *testing.T, c client.Client, name string, ready bool) {
	t.Helper()
	ctx := context.Background()
	var n corev1.Node
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &n))
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}
	require.NoError(t, c.Status().Update(ctx, &n)) //nolint:forbidigo // simulating the kubelet
}

// unschedulablePod is a pod of pool Job j that the scheduler has reported
// PodScheduled=False, reason Unschedulable, since since.
func unschedulablePod(t *testing.T, c client.Client, j *batchv1.Job, name string, since time.Time) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: j.Namespace, Labels: map[string]string{
			batchv1.JobNameLabel: j.Name, batchv1.ControllerUidLabel: string(j.UID),
		}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: pool.ContainerName, Image: "transcoder:test"}}},
	}
	require.NoError(t, c.Create(ctx, p))
	p.Status.Phase = corev1.PodPending
	p.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/1 nodes are available: 1 Insufficient nvidia.com/gpu.", LastTransitionTime: metav1.NewTime(since),
	}}
	require.NoError(t, c.Status().Update(ctx, p)) //nolint:forbidigo // simulating the scheduler
	return p
}

// appliedConstraint is the topology key pool Job j was created with. The
// envtest runs without WorkloadWithJob, which drops .spec.scheduling, so it
// is read from the applied-template annotation every pool apply carries.
func appliedConstraint(t *testing.T, j *batchv1.Job) string {
	t.Helper()
	raw, ok := j.Annotations[pool.AnnotationAppliedTemplate]
	require.True(t, ok, "pool %s has no applied-template annotation", j.Name)
	var s pool.Spec
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	return s.Constraint
}

// taskSubjects is every task subject stored for tp's pool of class.
func taskSubjects(t *testing.T, r *transcodejob.Reconciler, tp *transcodev1alpha1.TranscodeProfile, class string) []string {
	t.Helper()
	subs, err := r.Admin.Subjects(context.Background(), events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(tp.UID), class))
	require.NoError(t, err)
	return subs
}

// leaseOf is tj's lease as the KV holds it.
func leaseOf(t *testing.T, r *transcodejob.Reconciler, tj *transcodev1alpha1.TranscodeJob) task.Lease {
	t.Helper()
	e, err := r.Leases.Get(context.Background(), events.TranscodeLeaseKey(string(tj.UID)))
	require.NoError(t, err)
	var l task.Lease
	require.NoError(t, json.Unmarshal(e.Value, &l))
	return l
}

// TestAutoGoesToTheGPUPoolWhenOneIsFree is spec §18.5's "send a task to a
// GPU": an auto job, with a labelled Ready node that has an allocatable GPU
// and a free nvidia slot, is planned for nvidia at dispatch -- the task
// carries that plan's argsHash, the one the worker computes for the class --
// and goes to the profile's nvidia pool, a Job created with the class's GPU
// node label as its topology constraint.
func TestAutoGoesToTheGPUPoolWhenOneIsFree(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-auto-gpu"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	require.Equal(t, transcodev1alpha1.HardwareAuto, tp.Spec.Hardware, "the CRD default is auto")
	nvidiaNode(t, c, "gpu-1", "1")
	mf := newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 2, "nvidia": 1})

	reconcileTJ(t, r, ns, "heat-hevc")
	got := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, got.Status.Hardware)
	require.NotNil(t, got.Status.Plan)
	assert.Equal(t, "hevc_nvenc", got.Status.Plan.Encoder, "planned for the class it went to, in the Queued write")
	assert.Empty(t, got.Status.FallbackReason)
	require.NotNil(t, got.Status.JobRef)
	assert.Equal(t, poolKey(tp, transcodev1alpha1.HardwareNVIDIA).Name, *got.Status.JobRef)
	planned := k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
	require.NotNil(t, planned)
	assert.Contains(t, planned.Message, "hevc_nvenc", "the Planned condition names the plan status.plan records")

	tasks := takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second)
	require.Len(t, tasks, 1, "the task is on the nvidia pool's subject")
	tk := tasks[0]
	assert.Equal(t, "nvidia", tk.Class)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, *tk.Profile.Hardware)
	assert.Equal(t, got.Status.Plan.ArgsHash, tk.ArgsHash, "the task carries the plan the job records")
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "cpu", 300*time.Millisecond))

	// The argsHash is the worker's for this task: the same profile, class,
	// threads and output, from the same probe, with NVENC present.
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(mf), mf))
	info, err := transcode.FromSummary(tk.SourcePath, mf.Status.MediaInfo)
	require.NoError(t, err)
	info.Format.SizeBytes = mf.Spec.SizeBytes
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true, transcode.TierNVENC: true}}
	wp, err := transcode.Plan(info, worker.ProfileSpec(tk.Profile.Spec, tk.Profile.Hardware), caps, transcode.PlanMeta{
		ProfileName: tk.Profile.Name, ProfileHash: tk.Profile.Hash, Threads: pool.Threads(tp), OutputPath: tk.OutputPath,
	})
	require.NoError(t, err)
	assert.Equal(t, transcode.TierNVENC, wp.Tier)
	assert.Equal(t, transcode.ArgsHash(wp), tk.ArgsHash, "status.plan.argsHash is the hash of the argv the nvidia worker runs")

	gpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	assert.Equal(t, pool.DefaultNodeLabelNVIDIA, appliedConstraint(t, gpuPool),
		"the GPU pool Job is created with the class's GPU node label as its topology constraint")
	assert.False(t, *gpuPool.Spec.Suspend)
	pod := gpuPool.Spec.Template.Spec
	require.NotNil(t, pod.Affinity)
	assert.Equal(t, pool.DefaultNodeLabelNVIDIA,
		pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Contains(t, pod.Containers[0].Resources.Limits, pool.GPUResource[transcodev1alpha1.HardwareNVIDIA])
	assert.True(t, poolGone(t, c, tp, transcodev1alpha1.HardwareCPU), "nothing went to cpu")
}

// TestAutoFallsBackToCPUWithoutAGPUNodeOrSlot: without a GPU node an auto
// job is planned and dispatched for cpu; with one and a single nvidia slot,
// the pass that admits two auto jobs sends the first to nvidia and -- the
// slot counted as taken by this pass's own dispatch -- the second to cpu.
// A node that is not Ready, is cordoned, lacks the label or has no
// allocatable GPU is no GPU node.
func TestAutoFallsBackToCPUWithoutAGPUNodeOrSlot(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-auto-cpu"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	for _, name := range []string{"a", "b", "c"} {
		newMediaFile(t, c, ns, name, "p-"+name, ptr.To(h264Probe()))
	}
	r := newReconciler(t, c, map[string]int32{"cpu": 3, "nvidia": 1})

	// Nodes that do not count: not Ready, cordoned, unlabelled, no GPU.
	gpuNode(t, c, "not-ready", pool.DefaultNodeLabelNVIDIA, "nvidia.com/gpu", "1")
	setNodeReady(t, c, "not-ready", false)
	cordoned := gpuNode(t, c, "cordoned", pool.DefaultNodeLabelNVIDIA, "nvidia.com/gpu", "1")
	cordoned.Spec.Unschedulable = true
	require.NoError(t, c.Update(ctx, cordoned))
	gpuNode(t, c, "unlabelled", "example.com/other", "nvidia.com/gpu", "1")
	gpuNode(t, c, "no-gpu", pool.DefaultNodeLabelNVIDIA, "nvidia.com/gpu", "0")

	newTJ(t, c, ns, "a-hevc", "a", "hevc", "p-a", nil)
	reconcileTJ(t, r, ns, "a-hevc")
	a := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, a.Status.Phase, "message: %s", a.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, a.Status.Hardware, "no node counts as a GPU node")
	assert.Equal(t, "libx265", a.Status.Plan.Encoder)
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second), 1)

	// Both jobs Planned before one pass admits them together.
	nvidiaNode(t, c, "gpu-1", "1")
	newTJ(t, c, ns, "b-hevc", "b", "hevc", "p-b", nil)
	newTJ(t, c, ns, "c-hevc", "c", "hevc", "p-c", nil)
	slots := r.Slots
	r.Slots = map[string]int32{}
	reconcileTJ(t, r, ns, "b-hevc")
	reconcileTJ(t, r, ns, "c-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, c, ns, "b-hevc").Status.Phase)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, c, ns, "c-hevc").Status.Phase)
	r.Slots = slots
	admitPass(t, r)

	b, cj := getTJ(t, c, ns, "b-hevc"), getTJ(t, c, ns, "c-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, b.Status.Phase, "message: %s", b.Status.Message)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, cj.Status.Phase, "message: %s", cj.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, b.Status.Hardware, "the older job takes the one GPU slot")
	assert.Equal(t, "hevc_nvenc", b.Status.Plan.Encoder)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, cj.Status.Hardware, "the GPU slot is taken by this pass's own dispatch")
	assert.Equal(t, "libx265", cj.Status.Plan.Encoder)
	assert.Empty(t, cj.Status.FallbackReason, "a full GPU is not a fallback: the next attempt may take a GPU")
	assert.Len(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second), 1)
	assert.Len(t, takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second), 1)
}

// TestAGPUEncodeFailureMovesAnAutoJobToCPU is spec §18.3's GPU row for an
// auto job: GPUEncodeFailed on nvidia records a fallbackReason and requeues
// with no wait; the next dispatch is planned for cpu -- the new plan in
// status.plan and its argsHash on the task, not the GPU plan's (the Task 10
// review's CPU-fallback carry) -- and the job never returns to nvidia, even
// with a GPU slot free.
func TestAGPUEncodeFailureMovesAnAutoJobToCPU(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-auto-fallback"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	nvidiaNode(t, c, "gpu-1", "1")
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 1})
	now := time.Now()
	r.Now = func() time.Time { return now }

	reconcileTJ(t, r, ns, "heat-hevc")
	tj := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, tj.Status.Hardware)
	require.EqualValues(t, 1, tj.Status.Attempts)
	gpuHash := tj.Status.Plan.ArgsHash
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second), 1)

	require.NoError(t, deliver(t, r, tj, claimed(1, "pool-gpu")))
	require.NoError(t, deliver(t, r, tj, finished(1, task.OutcomeFailed, task.ReasonGPUEncodeFailed, "nvenc: no capable devices")))
	tj = getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, tj.Status.Phase)
	assert.Nil(t, tj.Status.NextAttemptAt, "a fallback does not wait")
	assert.Contains(t, tj.Status.FallbackReason, "GPUEncodeFailed")

	reconcileTJ(t, r, ns, "heat-hevc")
	tj = getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, tj.Status.Hardware)
	assert.EqualValues(t, 2, tj.Status.Attempts)
	assert.Equal(t, "libx265", tj.Status.Plan.Encoder, "re-planned for cpu in the Queued write")
	assert.NotEqual(t, gpuHash, tj.Status.Plan.ArgsHash)
	cpuTasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second)
	require.Len(t, cpuTasks, 1)
	assert.EqualValues(t, 2, cpuTasks[0].Attempt)
	assert.Equal(t, tj.Status.Plan.ArgsHash, cpuTasks[0].ArgsHash, "the cpu task carries the cpu plan's argsHash")

	// A retriable failure on cpu: the retry is cpu again, with the GPU free.
	require.NoError(t, deliver(t, r, tj, claimed(2, "pool-cpu")))
	require.NoError(t, deliver(t, r, tj, finished(2, task.OutcomeFailed, task.ReasonRetriable, "ffmpeg exited 1")))
	tj = getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, tj.Status.Phase)
	require.NotNil(t, tj.Status.NextAttemptAt)
	now = tj.Status.NextAttemptAt.Add(time.Second)
	reconcileTJ(t, r, ns, "heat-hevc")
	tj = getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	assert.EqualValues(t, 3, tj.Status.Attempts)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, tj.Status.Hardware, "a fallback reason pins cpu for good")
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 300*time.Millisecond))
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second), 1)
}

// TestAPinnedGPUJobNeverFallsBack: a profile pinned to nvidia retries a GPU
// failure on nvidia with a backoff and no fallbackReason, and its retry
// goes to nvidia -- even once no GPU node is left, since a pinned class
// never falls back (spec §18.5).
func TestAPinnedGPUJobNeverFallsBack(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-pinned-gpu"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "nvenc", "hash1", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.Hardware = transcodev1alpha1.HardwareNVIDIA
	})
	node := nvidiaNode(t, c, "gpu-1", "1")
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-nvenc", "heat", "nvenc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 1})
	now := time.Now()
	r.Now = func() time.Time { return now }

	reconcileTJ(t, r, ns, "heat-nvenc")
	tj := getTJ(t, c, ns, "heat-nvenc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, tj.Status.Hardware)
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second), 1)

	require.NoError(t, deliver(t, r, tj, claimed(1, "pool-gpu")))
	require.NoError(t, deliver(t, r, tj, finished(1, task.OutcomeFailed, task.ReasonGPUEncodeFailed, "nvenc: no capable devices")))
	tj = getTJ(t, c, ns, "heat-nvenc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, tj.Status.Phase)
	require.NotNil(t, tj.Status.NextAttemptAt, "a pinned GPU failure is a retry")
	assert.Empty(t, tj.Status.FallbackReason)

	require.NoError(t, c.Delete(ctx, node))
	now = tj.Status.NextAttemptAt.Add(time.Second)
	reconcileTJ(t, r, ns, "heat-nvenc")
	tj = getTJ(t, c, ns, "heat-nvenc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	assert.EqualValues(t, 2, tj.Status.Attempts)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, tj.Status.Hardware, "pinned: nvidia again, with no GPU node left")
	assert.Equal(t, "hevc_nvenc", tj.Status.Plan.Encoder)
	assert.Empty(t, tj.Status.FallbackReason)
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second), 1)
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "cpu", 300*time.Millisecond))
}

// TestAnAutoJobIsHeldOnlyForThePoolItWouldUse is ruling R4: admission
// assigns a class first and holds a job only for that class's pool. An auto
// job bound for nvidia is not held for its profile's Failed cpu pool; one
// bound for nvidia while that pool recovers is held for it; and once no GPU
// node is left, the same job is held for the cpu pool instead.
func TestAnAutoJobIsHeldOnlyForThePoolItWouldUse(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-r4"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	for _, name := range []string{"a", "b", "c"} {
		newMediaFile(t, c, ns, name, "p-"+name, ptr.To(h264Probe()))
	}
	r := newReconciler(t, c, map[string]int32{"cpu": 2, "nvidia": 2})

	newTJ(t, c, ns, "a-hevc", "a", "hevc", "p-a", nil)
	reconcileTJ(t, r, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.HardwareCPU, getTJ(t, c, ns, "a-hevc").Status.Hardware, "no GPU node yet")
	cpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareCPU)
	setPoolFailed(t, c, cpuPool)

	// Bound for nvidia: the cpu pool's failure is not its business.
	gpu := nvidiaNode(t, c, "gpu-1", "2")
	newTJ(t, c, ns, "b-hevc", "b", "hevc", "p-b", nil)
	reconcileTJ(t, r, ns, "b-hevc")
	b := getTJ(t, c, ns, "b-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, b.Status.Phase,
		"an auto job bound for nvidia is never held for a cpu pool it would not use: %s", b.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, b.Status.Hardware)
	require.True(t, poolGone(t, c, tp, transcodev1alpha1.HardwareCPU), "the Failed cpu pool was deleted, into its backoff")

	// Bound for nvidia while its nvidia pool recovers: held for it.
	gpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	setPoolFailed(t, c, gpuPool)
	newTJ(t, c, ns, "c-hevc", "c", "hevc", "p-c", nil)
	reconcileTJ(t, r, ns, "c-hevc")
	cj := getTJ(t, c, ns, "c-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, cj.Status.Phase)
	assert.Contains(t, cj.Status.Message, "waiting for pool "+gpuPool.Name, "held for the GPU pool it would use")
	assert.Zero(t, cj.Status.Attempts)

	// No GPU node left: the same job would use cpu, and is held for that.
	setNodeReady(t, c, gpu.Name, false)
	reconcileTJ(t, r, ns, "c-hevc")
	cj = getTJ(t, c, ns, "c-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, cj.Status.Phase)
	assert.Contains(t, cj.Status.Message, "waiting for pool "+cpuPool.Name,
		"the hold follows the class assigned this pass, not the GPU pool it no longer would use")
}

// TestAnUnschedulableGPUPoolReroutesItsQueuedJobs is spec §18.5's
// unschedulable pool: a pod of the nvidia pool unschedulable for more than
// 10 minutes marks the pool, and its Queued auto job is withdrawn -- its
// lease cancelled, its task purged -- and returned to Planned with a
// fallbackReason naming the pool, while a pinned job on the same pool stays
// where it is. The rerouted job is then dispatched to cpu, as a new attempt.
func TestAnUnschedulableGPUPoolReroutesItsQueuedJobs(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-unschedulable"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	nvidiaNode(t, c, "gpu-1", "2")
	newMediaFile(t, c, ns, "a", "p-a", ptr.To(h264Probe()))
	newMediaFile(t, c, ns, "p", "p-p", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "p-a", nil)
	newTJ(t, c, ns, "p-hevc", "p", "hevc", "p-p", func(tj *transcodev1alpha1.TranscodeJob) {
		tj.Spec.Hardware = ptr.To(transcodev1alpha1.HardwareNVIDIA) // pinned, under the auto profile
	})
	r := newReconciler(t, c, map[string]int32{"cpu": 0, "nvidia": 2})
	r.Admin = r.Bus.(events.StreamAdmin)
	rec := &capturingRecorder{}
	r.Recorder = rec

	reconcileTJ(t, r, ns, "a-hevc")
	reconcileTJ(t, r, ns, "p-hevc")
	a, p := getTJ(t, c, ns, "a-hevc"), getTJ(t, c, ns, "p-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, a.Status.Phase, "message: %s", a.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, a.Status.Hardware)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, p.Status.Phase, "message: %s", p.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, p.Status.Hardware)
	require.Len(t, taskSubjects(t, r, tp, "nvidia"), 2)

	gpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	setPoolRunning(t, c, gpuPool, 2)
	gpuPool = getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)

	// Not yet: nine minutes is inside the window.
	pod := unschedulablePod(t, c, gpuPool, "pool-pod-1", time.Now().Add(-9*time.Minute))
	admitPass(t, r)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "a-hevc").Status.Phase)
	require.Empty(t, rec.take(transcodejob.ReasonPoolUnschedulable))

	require.NoError(t, c.Delete(context.Background(), pod))
	unschedulablePod(t, c, gpuPool, "pool-pod-2", time.Now().Add(-11*time.Minute))
	admitPass(t, r)

	a = getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, a.Status.Phase, "message: %s", a.Status.Message)
	assert.EqualValues(t, 1, a.Status.Attempts, "the withdrawn attempt is the last one recorded")
	assert.Contains(t, a.Status.FallbackReason, "GPU pool "+gpuPool.Name+" unschedulable for 10m")
	assert.Contains(t, a.Status.Message, gpuPool.Name)
	assert.Nil(t, a.Status.NextAttemptAt)
	assert.Empty(t, a.Status.WorkerPod)
	jc := k8s.FindCondition(a.Status.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated)
	require.NotNil(t, jc)
	assert.Equal(t, metav1.ConditionFalse, jc.Status)
	assert.Equal(t, transcodejob.ReasonPoolUnschedulable, jc.Reason)
	lease := leaseOf(t, r, a)
	assert.Equal(t, task.LeaseCancelled, lease.State)
	assert.EqualValues(t, 1, lease.Attempt)
	assert.Equal(t, []string{events.WorkTranscodeTaskSubject(string(tp.UID), "nvidia", string(p.UID))},
		taskSubjects(t, r, tp, "nvidia"), "the auto job's task is purged; the pinned job's stays")

	p = getTJ(t, c, ns, "p-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, p.Status.Phase, "a pinned job never falls back")
	assert.Empty(t, p.Status.FallbackReason)

	warned := rec.take(transcodejob.ReasonPoolUnschedulable)
	require.Len(t, warned, 1)
	assert.Equal(t, corev1.EventTypeWarning, warned[0].eventType)
	on, ok := warned[0].regarding.(*transcodev1alpha1.TranscodeProfile)
	require.True(t, ok, "the Event is on the TranscodeProfile, not %T", warned[0].regarding)
	assert.Equal(t, tp.UID, on.UID)
	assert.Contains(t, warned[0].note, gpuPool.Name)

	admitPass(t, r)
	assert.Empty(t, rec.take(transcodejob.ReasonPoolUnschedulable), "one Event per mark, not per pass")

	// A cpu slot opens: the rerouted job goes there, as attempt 2.
	r.Slots["cpu"] = 1
	admitPass(t, r)
	a = getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, a.Status.Phase, "message: %s", a.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, a.Status.Hardware)
	assert.EqualValues(t, 2, a.Status.Attempts)
	assert.Equal(t, "libx265", a.Status.Plan.Encoder)
	cpuTasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second)
	require.Len(t, cpuTasks, 1)
	assert.EqualValues(t, 2, cpuTasks[0].Attempt)
	assert.Equal(t, a.Status.Plan.ArgsHash, cpuTasks[0].ArgsHash)

	// The pinned job finishes; nothing is dispatched to the pool, and it
	// suspends.
	runToSuccess(t, r, c, ns, "p-hevc")
	admitPass(t, r)
	assert.True(t, *getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA).Spec.Suspend)
}

// TestTheUnschedulableMarkLastsThirtyMinutesAndIsInMemory: a rerouted job
// is dispatched to cpu in the pass that reroutes it, and the pool, with
// nothing left dispatched, suspends in that pass. While the mark holds, an
// auto job goes to cpu with a GPU slot free; once it lapses, to nvidia. A
// restarted controller has forgotten the mark and finds the pool again from
// the pod's own transition time -- at once, not ten minutes later.
func TestTheUnschedulableMarkLastsThirtyMinutesAndIsInMemory(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-unschedulable-mark"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	nvidiaNode(t, c, "gpu-1", "2")
	for _, name := range []string{"a", "b", "d"} {
		newMediaFile(t, c, ns, name, "p-"+name, ptr.To(h264Probe()))
	}
	r := newReconciler(t, c, map[string]int32{"cpu": 2, "nvidia": 2})
	r.Admin = r.Bus.(events.StreamAdmin)
	t0 := time.Now()
	now := t0
	r.Now = func() time.Time { return now }

	newTJ(t, c, ns, "a-hevc", "a", "hevc", "p-a", nil)
	reconcileTJ(t, r, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, getTJ(t, c, ns, "a-hevc").Status.Hardware)
	gpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	setPoolRunning(t, c, gpuPool, 1)
	gpuPool = getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	pod := unschedulablePod(t, c, gpuPool, "pool-pod-1", t0.Add(-11*time.Minute))

	admitPass(t, r)
	a := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, a.Status.Phase, "rerouted and dispatched in one pass: %s", a.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, a.Status.Hardware)
	assert.EqualValues(t, 2, a.Status.Attempts)
	assert.NotEmpty(t, a.Status.FallbackReason)
	assert.True(t, *getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA).Spec.Suspend, "nothing is dispatched to it any more")
	// Attempt 2 was dispatched moments after attempt 1 was withdrawn, and
	// the cancel marker survives it: a GPU worker that fetched attempt 1
	// before the purge, and has yet to claim it, must still find it
	// cancelled. Attempt 2's worker replaces a marker from an earlier
	// attempt (squasharr/worker's claim).
	lease := leaseOf(t, r, a)
	assert.Equal(t, task.LeaseCancelled, lease.State, "the re-dispatch cleared the withdrawn attempt's cancel marker")
	assert.EqualValues(t, 1, lease.Attempt)

	// The Job controller acts on the suspend: the pods go.
	require.NoError(t, c.Delete(ctx, pod))
	setPoolStopped(t, c, getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA))

	now = t0.Add(time.Minute)
	newTJ(t, c, ns, "b-hevc", "b", "hevc", "p-b", nil)
	reconcileTJ(t, r, ns, "b-hevc")
	b := getTJ(t, c, ns, "b-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, b.Status.Phase, "message: %s", b.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, b.Status.Hardware, "a marked pool gets no auto job, GPU slot free or not")
	assert.Empty(t, b.Status.FallbackReason, "the mark is the pool's, not the job's")

	now = t0.Add(transcodejob.UnschedulableForTest + time.Minute)
	newTJ(t, c, ns, "d-hevc", "d", "hevc", "p-d", nil)
	reconcileTJ(t, r, ns, "d-hevc")
	d := getTJ(t, c, ns, "d-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, d.Status.Phase, "message: %s", d.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, d.Status.Hardware, "the mark lapsed after 30 minutes")

	// A restart: a new Reconciler over the same cluster and bus has no mark.
	// The pool runs again and its pod has waited since before the restart:
	// found at once.
	restarted := &transcodejob.Reconciler{
		Client: c, Slots: map[string]int32{"cpu": 2, "nvidia": 2}, Pool: r.Pool,
		Bus: r.Bus, Leases: r.Leases, Admin: r.Admin, Now: r.Now,
	}
	gpuPool = getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	require.False(t, *gpuPool.Spec.Suspend, "d resumed it")
	setPoolRunning(t, c, gpuPool, 1)
	unschedulablePod(t, c, getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA), "pool-pod-2", now.Add(-11*time.Minute))
	admitPass(t, restarted)
	d = getTJ(t, c, ns, "d-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, d.Status.Phase,
		"the restarted controller rerouted it at once (both cpu slots are taken): %s", d.Status.Message)
	assert.Contains(t, d.Status.FallbackReason, gpuPool.Name)
}

// racingKV is the lease bucket with a second writer: before the first Put
// -- withdraw's cancel marker -- it runs before, once, and it keeps every
// value Put.
type racingKV struct {
	events.KV
	once   sync.Once
	before func()
	mu     sync.Mutex
	puts   [][]byte
}

func (k *racingKV) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	k.once.Do(k.before)
	k.mu.Lock()
	k.puts = append(k.puts, val)
	k.mu.Unlock()
	return k.KV.Put(ctx, key, val)
}

// TestARerouteRacingAClaimDoesNotWedgeTheJob is ruling R24: a pod of the
// pool that did schedule claims the task between the pass listing the job
// Queued and withdrawing it, and the claim lands (Running) before the
// reroute's write. The cancel marker stops that worker whatever phase its
// claim reached, so the write must still return the job to Planned -- a
// write that refused a Running job would leave it Running with its worker
// cancelled, forever. The job goes to cpu as attempt 2, and the cancelled
// worker's own report is a no-op.
func TestARerouteRacingAClaimDoesNotWedgeTheJob(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-reroute-race"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	nvidiaNode(t, c, "gpu-1", "1")
	newMediaFile(t, c, ns, "a", "p-a", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "p-a", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 1})
	r.Admin = r.Bus.(events.StreamAdmin)

	reconcileTJ(t, r, ns, "a-hevc")
	a := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, a.Status.Phase, "message: %s", a.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, a.Status.Hardware)
	gpuPool := getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA)
	setPoolRunning(t, c, gpuPool, 2)
	unschedulablePod(t, c, getPool(t, c, tp, transcodev1alpha1.HardwareNVIDIA), "pool-pod-2", time.Now().Add(-11*time.Minute))

	leases := &racingKV{KV: r.Leases}
	leases.before = func() {
		// pool-pod-1 was scheduled, and claims the task first.
		require.NoError(t, deliver(t, r, a, claimed(1, "pool-pod-1")))
		require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, getTJ(t, c, ns, "a-hevc").Status.Phase)
	}
	r.Leases = leases
	admitPass(t, r)

	got := getTJ(t, c, ns, "a-hevc")
	require.NotEqual(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase,
		"wedged: Running with its worker cancelled (message: %s)", got.Status.Message)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, got.Status.Hardware)
	assert.EqualValues(t, 2, got.Status.Attempts)
	assert.Contains(t, got.Status.FallbackReason, gpuPool.Name)
	require.NotEmpty(t, leases.puts)
	var cancel task.Lease
	require.NoError(t, json.Unmarshal(leases.puts[0], &cancel))
	assert.Equal(t, task.LeaseCancelled, cancel.State)
	assert.EqualValues(t, 1, cancel.Attempt, "the claimed attempt is the one cancelled")
	assert.Equal(t, cancel, leaseOf(t, r, a), "and the marker still stands for pool-pod-1 to find at its renewal")

	// The cancelled worker reports; attempt 1 is over, so it changes nothing.
	require.NoError(t, deliver(t, r, a, finished(1, task.OutcomeCancelled, "", "cancelled by squasharr")))
	got = getTJ(t, c, ns, "a-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase)
	assert.EqualValues(t, 2, got.Status.Attempts)
	require.Len(t, takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second), 1)
}

// TestAReplanThatCannotUseTheChosenClassPublishesNothing covers the
// re-plan's other outcomes (dispatch, keepPlanned). A Dolby Vision source,
// which no hardware encoder writes, planned for the nvidia class admission
// chose, still encodes with libx265: nothing is published to the GPU pool,
// and the job stays Planned with that plan and a fallbackReason, so the next
// pass sends it to cpu. And a re-plan that decides to skip -- here the
// profile was edited after the job was planned -- is recorded Skipped, as
// plan records a skip, with nothing published.
func TestAReplanThatCannotUseTheChosenClassPublishesNothing(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-replan"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.HDR.DolbyVision = transcodev1alpha1.DolbyVisionDowngradeToHDR10
	})
	dv := dolbyVisionProbe()
	dv.VideoProfile, dv.PixelFormat, dv.VideoBitDepth = "Main", "yuv420p", 8 // not compliant: encoded
	newMediaFile(t, c, ns, "dv", "p-dv", &dv)
	newTJ(t, c, ns, "dv-hevc", "dv", "hevc", "p-dv", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 1})
	nvidiaNode(t, c, "gpu-1", "1")

	reconcileTJ(t, r, ns, "dv-hevc")
	got := getTJ(t, c, ns, "dv-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Zero(t, got.Status.Attempts, "nothing was dispatched")
	assert.Equal(t, "libx265", got.Status.Plan.Encoder)
	assert.Contains(t, got.Status.FallbackReason, "a plan for nvidia encodes with libx265")
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 300*time.Millisecond))

	reconcileTJ(t, r, ns, "dv-hevc")
	got = getTJ(t, c, ns, "dv-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, got.Status.Hardware)
	assert.EqualValues(t, 1, got.Status.Attempts)
	cpuTasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second)
	require.Len(t, cpuTasks, 1)
	assert.Equal(t, got.Status.Plan.ArgsHash, cpuTasks[0].ArgsHash)

	// A job planned for cpu while no slot was free, then its profile edited
	// to skip it: the re-plan for nvidia records the skip, and publishes
	// nothing.
	newMediaFile(t, c, ns, "short", "p-short", ptr.To(h264Probe()))
	newTJ(t, c, ns, "short-hevc", "short", "hevc", "p-short", nil)
	slots := r.Slots
	r.Slots = map[string]int32{}
	reconcileTJ(t, r, ns, "short-hevc")
	short := getTJ(t, c, ns, "short-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, short.Status.Phase, "message: %s", short.Status.Message)
	require.Equal(t, "libx265", short.Status.Plan.Encoder)
	editProfile(t, c, "hevc", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.Policy.MinDuration = &metav1.Duration{Duration: 10 * time.Hour}
	})
	r.Slots = slots
	reconcileTJ(t, r, ns, "short-hevc")
	short = getTJ(t, c, ns, "short-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSkipped, short.Status.Phase, "message: %s", short.Status.Message)
	assert.Equal(t, transcodev1alpha1.PlanModeSkip, short.Status.Plan.Mode)
	assert.Zero(t, short.Status.Attempts)
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "nvidia", 300*time.Millisecond))
}
