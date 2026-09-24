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

package pool

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func startEnv(t *testing.T, gates bool) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	if gates {
		env.ControlPlane.GetAPIServer().Configure().
			Append("feature-gates", "WorkloadWithJob=true,GenericWorkload=true").
			Append("runtime-config", "scheduling.k8s.io/v1alpha3=true")
	}
	restCfg, err := env.Start() // not `cfg`: that is the package's pool.Config
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(restCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	require.NoError(t, c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Namespace}}))
	return c
}

// newProfile creates a real TranscodeProfile so the owner reference resolves.
func newProfile(t *testing.T, c client.Client) *transcodev1alpha1.TranscodeProfile {
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc.uhd"}}
	tp.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}
	require.NoError(t, c.Create(context.Background(), tp))
	return tp
}

func apply(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile, d Desired, stored *batchv1.Job) error {
	t.Helper()
	k := Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}
	ac, err := Render(k, tp, Want(tp, "cpu", cfg), d, stored, cfg)
	require.NoError(t, err)
	_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
	return err
}

func get(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile) *batchv1.Job {
	t.Helper()
	var j batchv1.Job
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: cfg.Namespace, Name: Name(Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"})}, &j))
	return &j
}

// As the Job controller would: running pods, then a suspend that clears startTime.
func setRunning(t *testing.T, c client.Client, j *batchv1.Job, active int32) {
	j.Status.StartTime, j.Status.Active = &metav1.Time{Time: time.Now()}, active
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

func setStopped(t *testing.T, c client.Client, j *batchv1.Job) {
	j.Status.StartTime, j.Status.Active = nil, 0
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

func TestPoolLifecycleWithGangScheduling(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 2}, nil))
	j := get(t, c, tp)
	assert.Equal(t, int32(2), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	setRunning(t, c, j, 2)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 4}, get(t, c, tp)), "scale up while running")
	assert.Equal(t, int32(4), *get(t, c, tp).Spec.Scheduling.SchedulingPolicy.Gang.MinCount)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 4, Suspend: true}, get(t, c, tp)), "suspend to zero")
	setStopped(t, c, get(t, c, tp))

	tp.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	stored := get(t, c, tp)
	require.Equal(t, DriftReshape, Classify(stored, Want(tp, "cpu", cfg)))
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 1}, stored), "reshape and resume in one apply")
	got := get(t, c, tp)
	assert.Equal(t, "8", got.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String())
	assert.False(t, *got.Spec.Suspend)
	assert.Equal(t, DriftNone, Classify(got, Want(tp, "cpu", cfg)))

	matched := 0
	for _, mf := range got.ManagedFields {
		if mf.Manager != string(k8s.ManagerSquasharrPool) {
			continue
		}
		matched++
		raw := mf.FieldsV1.GetRawString()
		assert.Contains(t, raw, `"f:minCount"`)
		for _, other := range []string{`"f:schedulingConstraints"`, `"f:disruptionMode"`, `"f:resourceClaims"`} {
			assert.NotContains(t, raw, other)
		}
	}
	assert.NotZero(t, matched, "expected at least one managedFields entry for squasharr-pool")
}

// Review Focus 5.
func TestProfileEditWhileRunningNeverRejectsAnApply(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 1}, nil))
	setRunning(t, c, get(t, c, tp), 1)

	tp.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	stored := get(t, c, tp)
	assert.Equal(t, DriftReshape, Classify(stored, Want(tp, "cpu", cfg)))
	d, act := Next(stored, 1, DriftReshape)
	assert.Equal(t, ActionNone, act, "a busy pool holds while draining")
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: d.Parallelism}, stored),
		"rendering a running pool re-sends its applied template, so the apply is accepted")
	assert.Equal(t, "4", get(t, c, tp).Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String())
}

func TestPoolWithoutTheGate(t *testing.T) {
	c := startEnv(t, false)
	tp := newProfile(t, c)
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 2}, nil))
	assert.Nil(t, get(t, c, tp).Spec.Scheduling, "the apiserver drops minCount without WorkloadWithJob")
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 3}, get(t, c, tp)))
	assert.Equal(t, int32(3), *get(t, c, tp).Spec.Parallelism)
}

func TestGPUPoolConstraintAgainstTheApiserver(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	k := Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "nvidia"}
	get := func() *batchv1.Job {
		var j batchv1.Job
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: Name(k)}, &j))
		return &j
	}
	put := func(d Desired, stored *batchv1.Job) error {
		ac, err := Render(k, tp, Want(tp, k.Class, cfg), d, stored, cfg)
		require.NoError(t, err)
		_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
		return err
	}
	require.NoError(t, put(Desired{Parallelism: 1, Suspend: true}, nil))
	assert.Equal(t, "nvidia.com/gpu.present", get().Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	require.NoError(t, put(Desired{Parallelism: 2}, get()), "every later apply re-sends the constraint it was created with")
	relabelled := cfg
	relabelled.NodeLabelNVIDIA = "example.com/gpu"
	assert.Equal(t, DriftRecreate, Classify(get(), Want(tp, k.Class, relabelled)), "never applied in place")
}

func TestAGateEnabledLaterReadsAsRecreate(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	// A pool created before the gate existed: no .spec.scheduling.
	ac, err := Render(Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}, tp, Want(tp, "cpu", cfg), Desired{Parallelism: 1}, nil, cfg)
	require.NoError(t, err)
	ac.Spec.Scheduling = nil
	_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
	require.NoError(t, err)

	err = apply(t, c, tp, Desired{Parallelism: 1}, get(t, c, tp))
	require.Error(t, err)
	assert.True(t, IsSchedulingImmutable(err), "%v", err)
}

// TestAnOlderPodFailurePolicyReadsAsRecreateOnly is final-review I1's
// upgrade path. A Job's podFailurePolicy is immutable, so a pool created
// before the exit-137 rule refuses every apply that carries the new policy
// -- even a suspend -- and IsRecreateOnly must say so, or the reconciler
// would retry that apply forever and the profile's work would wedge.
func TestAnOlderPodFailurePolicyReadsAsRecreateOnly(t *testing.T) {
	c := startEnv(t, false)
	tp := newProfile(t, c)
	k := Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}
	ac, err := Render(k, tp, Want(tp, "cpu", cfg), Desired{Parallelism: 1}, nil, cfg)
	require.NoError(t, err)
	// The pool as the release before I1 created it: exit 10 ignored, 137 not.
	require.Equal(t, []int32{worker.WorkerExitDrained, ExitOOMKilled}, ac.Spec.PodFailurePolicy.Rules[1].OnExitCodes.Values)
	ac.Spec.PodFailurePolicy.Rules[1].OnExitCodes.Values = []int32{worker.WorkerExitDrained}
	_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
	require.NoError(t, err)

	err = apply(t, c, tp, Desired{Parallelism: 1, Suspend: true}, get(t, c, tp))
	require.Error(t, err, "the apiserver never lets a Job's podFailurePolicy change")
	assert.True(t, IsPodFailurePolicyImmutable(err), "%v", err)
	assert.True(t, IsRecreateOnly(err), "%v", err)
	assert.False(t, IsSchedulingImmutable(err), "%v", err)
}
