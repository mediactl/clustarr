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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/worker"
)

func gpuProfile(hw transcodev1alpha1.Hardware) *transcodev1alpha1.TranscodeProfile {
	return &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu"},
		Spec: transcodev1alpha1.TranscodeProfileSpec{
			Hardware: hw,
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
			}},
			GPU: &transcodev1alpha1.GPUSpec{
				Count: 2, RuntimeClassName: "nvidia-cdi",
				NodeSelector: map[string]string{"gpu": "yes"},
				Tolerations:  []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
			},
			Scratch: resource.MustParse("20Gi"),
		},
	}
}

func testTJ() *transcodev1alpha1.TranscodeJob {
	return &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "movie-abcdef", Namespace: "media", UID: "uid-1"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{ProfileRef: "gpu", MediaFileRef: "movie"},
	}
}

func TestBuildJobNVIDIA(t *testing.T) {
	cfg := JobConfig{Image: "cpu:1", ImageCUDA: "cuda:1"}
	job := buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareNVIDIA), transcodev1alpha1.HardwareNVIDIA, cfg)
	pod := job.Spec.Template.Spec
	ctr := pod.Containers[0]

	assert.Equal(t, "cuda:1", ctr.Image, "the nvidia tier runs the CUDA image")
	assert.EqualValues(t, 2, ptrQ(ctr.Resources.Limits, resourceNVIDIAGPU))
	assert.EqualValues(t, 4, ptrQ(ctr.Resources.Limits, corev1.ResourceCPU))
	require.NotNil(t, pod.RuntimeClassName)
	assert.Equal(t, "nvidia-cdi", *pod.RuntimeClassName)
	require.NotNil(t, pod.Affinity)
	assert.Equal(t, nodeLabelNVIDIA,
		pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Equal(t, map[string]string{"gpu": "yes"}, pod.NodeSelector)
	assert.Len(t, pod.Tolerations, 1)
	var caps string
	for _, e := range ctr.Env {
		if e.Name == "NVIDIA_DRIVER_CAPABILITIES" {
			caps = e.Value
		}
	}
	assert.Equal(t, "video,compute,utility", caps)
	assert.Equal(t, "nvidia", job.Labels[LabelHardware])
	assert.LessOrEqual(t, len(job.Name), 63)
	assert.True(t, *job.Spec.Suspend)
}

func TestBuildJobIntel(t *testing.T) {
	job := buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareIntel), transcodev1alpha1.HardwareIntel, JobConfig{Image: "cpu:1", ImageCUDA: "cuda:1"})
	ctr := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "cpu:1", ctr.Image)
	assert.EqualValues(t, 2, ptrQ(ctr.Resources.Limits, resourceIntelGPU))
	assert.Nil(t, job.Spec.Template.Spec.RuntimeClassName)
}

// A Dolby Vision source under a GPU profile is planned onto the CPU tier;
// its Job must not request a GPU, pin to GPU nodes or use the CUDA image.
func TestBuildJobCPUUnderGPUProfile(t *testing.T) {
	job := buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareNVIDIA), transcodev1alpha1.HardwareCPU, JobConfig{Image: "cpu:1", ImageCUDA: "cuda:1"})
	pod := job.Spec.Template.Spec
	assert.Equal(t, "cpu:1", pod.Containers[0].Image)
	_, hasGPU := pod.Containers[0].Resources.Limits[resourceNVIDIAGPU]
	assert.False(t, hasGPU)
	assert.Nil(t, pod.RuntimeClassName)
	assert.Nil(t, pod.Affinity)
	assert.Empty(t, pod.NodeSelector)
	assert.Empty(t, pod.Tolerations)
}

func TestBuildJobDoesNotMutateTheProfile(t *testing.T) {
	p := gpuProfile(transcodev1alpha1.HardwareNVIDIA)
	buildJob(testTJ(), p, transcodev1alpha1.HardwareNVIDIA, JobConfig{})
	_, leaked := p.Spec.Resources.Limits[resourceNVIDIAGPU]
	assert.False(t, leaked, "the GPU limit must be added to a copy of the profile's resources")
}

// A Go client sends activeDeadline, resources and scratch present-but-zero,
// so the CRD defaults never reach them; the Job must get the defaults
// anyway, not "no deadline, no limits, unbounded scratch".
func TestBuildJobFloorsZeroProfileSchedulingFields(t *testing.T) {
	zero := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "zero"}}
	job := buildJob(testTJ(), zero, transcodev1alpha1.HardwareCPU, JobConfig{Image: "cpu:1"})

	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int64(48*3600), *job.Spec.ActiveDeadlineSeconds)
	ctr := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "8", ctr.Resources.Limits.Cpu().String())
	assert.Equal(t, "4Gi", ctr.Resources.Limits.Memory().String())
	var scratch *resource.Quantity
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == scratchVolumeName {
			scratch = v.EmptyDir.SizeLimit
		}
	}
	require.NotNil(t, scratch, "the scratch emptyDir must always have a sizeLimit")
	assert.Equal(t, "20Gi", scratch.String())

	// A negative deadline or scratch is as meaningless as a zero one.
	neg := zero.DeepCopy()
	neg.Spec.ActiveDeadline = metav1.Duration{Duration: -time.Hour}
	neg.Spec.Scratch = resource.MustParse("-1Gi")
	job = buildJob(testTJ(), neg, transcodev1alpha1.HardwareCPU, JobConfig{Image: "cpu:1"})
	assert.Equal(t, int64(48*3600), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, "20Gi", job.Spec.Template.Spec.Volumes[1].EmptyDir.SizeLimit.String())
}

// Only an entirely empty value is floored. What an operator set is theirs:
// a memory-only limit stays memory-only, and explicit values pass through.
func TestBuildJobKeepsSetSchedulingFields(t *testing.T) {
	p := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "set"},
		Spec: transcodev1alpha1.TranscodeProfileSpec{
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			}},
			ActiveDeadline: metav1.Duration{Duration: 3 * time.Hour},
			Scratch:        resource.MustParse("5Gi"),
		},
	}
	job := buildJob(testTJ(), p, transcodev1alpha1.HardwareCPU, JobConfig{Image: "cpu:1"})
	ctr := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, int64(3*3600), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, "2Gi", ctr.Resources.Limits.Memory().String())
	_, hasCPU := ctr.Resources.Limits[corev1.ResourceCPU]
	assert.False(t, hasCPU, "a set resources block is not merged with the default")
	assert.Equal(t, "5Gi", job.Spec.Template.Spec.Volumes[1].EmptyDir.SizeLimit.String())
}

func TestHardwareForEncoder(t *testing.T) {
	for enc, want := range map[string]transcodev1alpha1.Hardware{
		"libx265": "cpu", "copy": "cpu", "hevc_nvenc": "nvidia", "hevc_qsv": "intel", "hevc_vaapi": "intel", "": "cpu",
	} {
		assert.Equal(t, want, hardwareForEncoder(enc), enc)
	}
}

func TestStatusPlanRemuxTakesACPUSlot(t *testing.T) {
	p := statusPlan(&transcode.PlanResult{Decision: transcode.DecisionRemuxOnly, Tier: transcode.TierNVENC, VideoArgs: []string{"-c:v", "copy"}})
	assert.Equal(t, transcodev1alpha1.PlanModeRemuxOnly, p.Mode)
	assert.Equal(t, "copy", p.Encoder)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, hardwareForEncoder(p.Encoder))
}

func ptrQ(l corev1.ResourceList, name corev1.ResourceName) int64 {
	q, ok := l[name]
	if !ok {
		return -1
	}
	return q.Value()
}

// A Job carries the traceparent of the reconcile that built it, for the
// worker to continue; none is invented when there is no span.
func TestBuildJobCarriesTheTraceParent(t *testing.T) {
	env := func(cfg JobConfig) map[string]string {
		out := map[string]string{}
		for _, e := range buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareCPU), transcodev1alpha1.HardwareCPU, cfg).
			Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	tp := "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"
	assert.Equal(t, tp, env(JobConfig{TraceParent: tp})[worker.TraceParentEnv])
	_, set := env(JobConfig{})[worker.TraceParentEnv]
	assert.False(t, set)
}
