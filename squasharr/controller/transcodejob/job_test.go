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
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

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

// A Job's CLUSTARR_CPU_LIMIT is the pool size its plan was made with: the
// Downward API's limits.cpu (rounded up) when the container has a CPU limit,
// and otherwise a literal -- the CPU request rounded up, else the default
// profile's 8 cores -- never the node's allocatable CPU, which the Downward
// API would report and no plan can know.
func TestJobCPULimitEnvIsThePlannedThreads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resources corev1.ResourceRequirements
		threads   int32
		literal   bool
	}{
		{name: "no resources: the floored default limit", threads: 8},
		{name: "a fractional limit rounds up", resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2500m")},
		}, threads: 3},
		{name: "a memory-only limit takes the default", resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
		}, threads: 8, literal: true},
		{name: "a request without a limit takes the request", resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
		}, threads: 2, literal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &transcodev1alpha1.TranscodeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "p"},
				Spec:       transcodev1alpha1.TranscodeProfileSpec{Resources: tc.resources},
			}
			require.Equal(t, tc.threads, threadsFor(p))
			ctr := buildJob(testTJ(), p, transcodev1alpha1.HardwareCPU, JobConfig{Image: "cpu:1"}).Spec.Template.Spec.Containers[0]
			var env *corev1.EnvVar
			for i := range ctr.Env {
				if ctr.Env[i].Name == worker.CPULimitEnv {
					env = &ctr.Env[i]
				}
			}
			require.NotNil(t, env)
			if tc.literal {
				assert.Nil(t, env.ValueFrom, "no CPU limit: the Downward API would report the node's CPUs")
				assert.Equal(t, strconv.Itoa(int(tc.threads)), env.Value)
				return
			}
			require.NotNil(t, env.ValueFrom)
			require.NotNil(t, env.ValueFrom.ResourceFieldRef)
			assert.Equal(t, "limits.cpu", env.ValueFrom.ResourceFieldRef.Resource)
			assert.Equal(t, "1", env.ValueFrom.ResourceFieldRef.Divisor.String())
			cpu := ctr.Resources.Limits[corev1.ResourceCPU]
			assert.Equal(t, int64(tc.threads), (cpu.MilliValue()+999)/1000, "the Downward API renders the planned threads")
		})
	}
}

// The floors restate three +kubebuilder:default values. Read the generated
// schema, which is what is installed, so the two cannot drift apart.
func TestFlooredDefaultsMatchTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../config/crd/bases/transcode.clustarr.io_transcodeprofiles.yaml")
	require.NoError(t, err)
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Default any `json:"default"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")
	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties

	d, err := time.ParseDuration(props["activeDeadline"].Default.(string))
	require.NoError(t, err)
	assert.Equal(t, worker.DefaultActiveDeadline, d, "worker.DefaultActiveDeadline no longer mirrors spec.activeDeadline's default")
	assert.Equal(t, defaultScratch.String(), props["scratch"].Default, "defaultScratch no longer mirrors spec.scratch's default")

	var want corev1.ResourceRequirements
	b, err := yaml.Marshal(props["resources"].Default)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(b, &want))
	assert.True(t, equality.Semantic.DeepEqual(want, defaultResources()),
		"defaultResources() = %v no longer mirrors spec.resources' default %v", defaultResources(), want)
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

// Every transcode Job pod runs with the security settings every Deployment
// has: non-root as the images' clustarr user, RuntimeDefault seccomp, a
// read-only root filesystem with /tmp as a volume, no capabilities, and no
// privilege escalation -- whatever the hardware class.
func TestBuildJobPodSecurity(t *testing.T) {
	for _, hw := range []transcodev1alpha1.Hardware{
		transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
	} {
		t.Run(string(hw), func(t *testing.T) {
			pod := buildJob(testTJ(), gpuProfile(hw), hw, JobConfig{Image: "cpu:1"}).Spec.Template.Spec
			psc := pod.SecurityContext
			require.NotNil(t, psc, "the pod has no securityContext")
			require.NotNil(t, psc.RunAsNonRoot)
			assert.True(t, *psc.RunAsNonRoot)
			assert.Equal(t, int64(1000), *psc.RunAsUser)
			assert.Equal(t, int64(1000), *psc.RunAsGroup)
			assert.Equal(t, int64(1000), *psc.FSGroup)
			assert.Equal(t, corev1.FSGroupChangeOnRootMismatch, *psc.FSGroupChangePolicy)
			require.NotNil(t, psc.SeccompProfile)
			assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, psc.SeccompProfile.Type)

			csc := pod.Containers[0].SecurityContext
			require.NotNil(t, csc, "the container has no securityContext")
			assert.True(t, *csc.ReadOnlyRootFilesystem)
			assert.False(t, *csc.AllowPrivilegeEscalation)
			assert.Equal(t, []corev1.Capability{"ALL"}, csc.Capabilities.Drop)

			var tmpMounted bool
			for _, m := range pod.Containers[0].VolumeMounts {
				tmpMounted = tmpMounted || (m.Name == tmpVolumeName && m.MountPath == "/tmp")
			}
			assert.True(t, tmpMounted, "a read-only root filesystem needs /tmp as a volume")
		})
	}
}

// "Matching the Deployments" is a claim about config/manager, so read it:
// the Job pod's two security contexts must equal squasharr's own.
func TestJobPodSecurityMatchesTheDeployments(t *testing.T) {
	raw, err := os.ReadFile("../../../config/manager/squasharr.yaml")
	require.NoError(t, err)
	var deploy *appsv1.Deployment
	for _, doc := range strings.Split(string(raw), "\n---") {
		var d appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &d) == nil && d.Kind == "Deployment" {
			deploy = &d
		}
	}
	require.NotNil(t, deploy, "config/manager/squasharr.yaml has no Deployment")

	pod := buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareCPU), transcodev1alpha1.HardwareCPU, JobConfig{}).Spec.Template.Spec
	assert.True(t, equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.SecurityContext, pod.SecurityContext),
		"pod securityContext\n job:        %+v\n deployment: %+v", pod.SecurityContext, deploy.Spec.Template.Spec.SecurityContext)
	assert.True(t, equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.Containers[0].SecurityContext, pod.Containers[0].SecurityContext),
		"container securityContext\n job:        %+v\n deployment: %+v",
		pod.Containers[0].SecurityContext, deploy.Spec.Template.Spec.Containers[0].SecurityContext)
}

// §6.4's "supplementalGroups render": an Intel Job's non-root user reaches
// /dev/dri/renderD* through the host render group the operator names. Only
// the Intel tier gets it -- a CPU or NVIDIA pod has no /dev/dri to open.
func TestBuildJobIntelGetsTheRenderGroups(t *testing.T) {
	cfg := JobConfig{Image: "cpu:1", IntelRenderGroups: []int64{109, 44}}

	intel := buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareIntel), transcodev1alpha1.HardwareIntel, cfg)
	assert.Equal(t, []int64{109, 44}, intel.Spec.Template.Spec.SecurityContext.SupplementalGroups)

	for _, hw := range []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA} {
		job := buildJob(testTJ(), gpuProfile(hw), hw, cfg)
		assert.Empty(t, job.Spec.Template.Spec.SecurityContext.SupplementalGroups, "%s must not get the render groups", hw)
	}

	cfg.IntelRenderGroups[0] = 1
	assert.Equal(t, []int64{109, 44}, intel.Spec.Template.Spec.SecurityContext.SupplementalGroups,
		"the Job must hold its own copy of the configured groups")
}

// §11: the worker gets the Deployment's UMASK.
func TestBuildJobPassesUmask(t *testing.T) {
	env := func(cfg JobConfig) map[string]string {
		out := map[string]string{}
		for _, e := range buildJob(testTJ(), gpuProfile(transcodev1alpha1.HardwareCPU), transcodev1alpha1.HardwareCPU, cfg).
			Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	assert.Equal(t, "002", env(JobConfig{Umask: "002"})[UmaskEnv])
	_, set := env(JobConfig{})[UmaskEnv]
	assert.False(t, set, "no UMASK is invented when the controller has none")
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
