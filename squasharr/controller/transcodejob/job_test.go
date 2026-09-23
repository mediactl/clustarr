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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
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
