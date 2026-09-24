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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/squasharr/worker"
)

var cfg = Config{
	Namespace: "clustarr-system", Image: "transcoder:t", ImageCUDA: "transcoder-cuda:t",
	DataClaimName: "clustarr-data", DataDir: "/data", NATSURL: "nats://nats:4222", Umask: "002",
}

func profile() *transcodev1alpha1.TranscodeProfile {
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc.uhd", UID: "puid"}}
	tp.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}
	return tp
}

// rendered decodes an apply configuration back into a typed Job for assertions.
func rendered(t *testing.T, k Key, tp *transcodev1alpha1.TranscodeProfile, d Desired, stored *batchv1.Job) batchv1.Job {
	t.Helper()
	ac, err := Render(k, tp, Want(tp, k.Class, cfg), d, stored, cfg)
	require.NoError(t, err)
	b, err := json.Marshal(ac)
	require.NoError(t, err)
	var j batchv1.Job
	require.NoError(t, json.Unmarshal(b, &j))
	return j
}

func TestRenderIsACompletePoolDeclaration(t *testing.T) {
	k := Key{Profile: "hevc.uhd", ProfileUID: "puid", Class: transcodev1alpha1.HardwareNVIDIA}
	j := rendered(t, k, profile(), Desired{Parallelism: 3}, nil)

	assert.Equal(t, "batch/v1", j.APIVersion)
	assert.LessOrEqual(t, len(j.Name), 63)
	assert.True(t, strings.HasPrefix(j.Name, "squasharr-pool-"))
	assert.Equal(t, "clustarr-system", j.Namespace)
	require.Len(t, j.OwnerReferences, 1)
	assert.Equal(t, "TranscodeProfile", j.OwnerReferences[0].Kind)
	assert.True(t, *j.OwnerReferences[0].Controller)

	assert.Equal(t, int32(3), *j.Spec.Parallelism)
	assert.Nil(t, j.Spec.Completions, "work-queue pattern: completions unset")
	assert.Equal(t, batchv1.NonIndexedCompletion, *j.Spec.CompletionMode)
	assert.Equal(t, batchv1.Failed, *j.Spec.PodReplacementPolicy)
	assert.Equal(t, BackoffLimit, *j.Spec.BackoffLimit)
	assert.Nil(t, j.Spec.ActiveDeadlineSeconds)
	assert.Nil(t, j.Spec.TTLSecondsAfterFinished)
	require.NotNil(t, j.Spec.Scheduling)
	assert.Equal(t, int32(3), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	require.NotNil(t, j.Spec.Scheduling.SchedulingConstraints, "a GPU pool is created with its GPU label as a topology constraint (spec §18.5)")
	assert.Equal(t, "nvidia.com/gpu.present", j.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	assert.Nil(t, j.Spec.Scheduling.DisruptionMode)
	assert.Empty(t, j.Spec.Scheduling.ResourceClaims)

	rules := j.Spec.PodFailurePolicy.Rules
	require.Len(t, rules, 3)
	assert.Equal(t, batchv1.PodFailurePolicyActionIgnore, rules[0].Action)
	assert.Equal(t, []int32{worker.WorkerExitDrained}, rules[1].OnExitCodes.Values)
	assert.Equal(t, batchv1.PodFailurePolicyActionFailJob, rules[2].Action)
	assert.Equal(t, []int32{worker.WorkerExitMisconfigured}, rules[2].OnExitCodes.Values)

	pod := j.Spec.Template.Spec
	assert.False(t, *pod.AutomountServiceAccountToken, "the worker holds no Kubernetes credentials")
	assert.Empty(t, pod.ServiceAccountName)
	assert.Equal(t, "transcoder-cuda:t", pod.Containers[0].Image)
	assert.Equal(t, "nvidia", *pod.RuntimeClassName)
	envs := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		envs[e.Name] = e.Value
	}
	assert.Equal(t, "puid", envs[EnvProfileUID])
	assert.Equal(t, "nvidia", envs[EnvClass])
	assert.Equal(t, "nats://nats:4222", envs["NATS_URL"])
	assert.NotEmpty(t, j.Labels[LabelTemplateHash])
	assert.NotEmpty(t, j.Annotations[AnnotationAppliedTemplate])
}

func TestRenderNeverZeroesParallelism(t *testing.T) {
	j := rendered(t, Key{Profile: "p", ProfileUID: "u", Class: "cpu"}, profile(), Desired{Suspend: true}, nil)
	assert.Equal(t, int32(1), *j.Spec.Parallelism)
	assert.Equal(t, int32(1), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	assert.True(t, *j.Spec.Suspend)
	assert.Nil(t, j.Spec.Scheduling.SchedulingConstraints, "a CPU pool has no constraint")
}

func TestGPUPoolsCarryTheirNodeLabelAsASchedulingConstraint(t *testing.T) {
	intel := rendered(t, Key{Profile: "p", ProfileUID: "u", Class: "intel"}, profile(), Desired{Parallelism: 1}, nil)
	assert.Equal(t, "intel.feature.node.kubernetes.io/gpu", intel.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	aff := intel.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	assert.Equal(t, "intel.feature.node.kubernetes.io/gpu", aff.NodeSelectorTerms[0].MatchExpressions[0].Key,
		"pod affinity keeps the same label for clusters without WorkloadWithJob")

	k := Key{Profile: "p", ProfileUID: "u", Class: "nvidia"}
	stored := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	relabelled := cfg
	relabelled.NodeLabelNVIDIA = "example.com/gpu"
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, relabelled)),
		"the constraint is immutable: a new label key recreates the pool")

	// While the pool exists, the constraint it was created with is re-sent, never the new one.
	ac, err := Render(k, profile(), Want(profile(), k.Class, relabelled), Desired{Parallelism: 1, Suspend: true}, &stored, relabelled)
	require.NoError(t, err)
	assert.Equal(t, "nvidia.com/gpu.present", *ac.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
}

// A running pool is rendered with the template it was last applied with, so
// no apply ever asks the apiserver for a template change it would reject.
func TestRenderKeepsTheAppliedTemplateWhileRunning(t *testing.T) {
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	first := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	stored := first.DeepCopy()
	stored.Spec.Suspend = ptr.To(false)
	stored.Status.StartTime = &metav1.Time{}

	edited := profile()
	edited.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	next := rendered(t, k, edited, Desired{Parallelism: 2}, stored)
	assert.Equal(t, first.Spec.Template.Spec.Containers[0].Resources, next.Spec.Template.Spec.Containers[0].Resources)
	assert.Equal(t, int32(2), *next.Spec.Parallelism)
}
