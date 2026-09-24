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
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Every transcode pool pod runs with the security settings every Deployment
// has: non-root as the images' clustarr user, RuntimeDefault seccomp, a
// read-only root filesystem with /tmp as a volume, no capabilities, and no
// privilege escalation -- whatever the hardware class.
func TestBuildJobPodSecurity(t *testing.T) {
	for _, hw := range []transcodev1alpha1.Hardware{
		transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
	} {
		t.Run(string(hw), func(t *testing.T) {
			pod := Template(profile(), hw, Config{Image: "cpu:1"}).Spec
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
				tmpMounted = tmpMounted || (m.Name == TmpVolumeName && m.MountPath == "/tmp")
			}
			assert.True(t, tmpMounted, "a read-only root filesystem needs /tmp as a volume")
		})
	}
}

// "Matching the Deployments" is a claim about config/manager, so read it: the
// pool pod's two security contexts must equal squasharr's own.
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

	pod := Template(profile(), transcodev1alpha1.HardwareCPU, Config{}).Spec
	assert.True(t, equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.SecurityContext, pod.SecurityContext),
		"pod securityContext\n job:        %+v\n deployment: %+v", pod.SecurityContext, deploy.Spec.Template.Spec.SecurityContext)
	assert.True(t, equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.Containers[0].SecurityContext, pod.Containers[0].SecurityContext),
		"container securityContext\n job:        %+v\n deployment: %+v",
		pod.Containers[0].SecurityContext, deploy.Spec.Template.Spec.Containers[0].SecurityContext)
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

// §6.4's "supplementalGroups render": an Intel pod's non-root user reaches
// /dev/dri/renderD* through the host render group the operator names. Only
// the Intel tier gets it -- a CPU or NVIDIA pod has no /dev/dri to open.
func TestBuildJobIntelGetsTheRenderGroups(t *testing.T) {
	c := Config{Image: "cpu:1", IntelRenderGroups: []int64{109, 44}}

	intel := Template(profile(), transcodev1alpha1.HardwareIntel, c)
	assert.Equal(t, []int64{109, 44}, intel.Spec.SecurityContext.SupplementalGroups)

	for _, hw := range []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA} {
		pod := Template(profile(), hw, c)
		assert.Empty(t, pod.Spec.SecurityContext.SupplementalGroups, "%s must not get the render groups", hw)
	}

	c.IntelRenderGroups[0] = 1
	assert.Equal(t, []int64{109, 44}, intel.Spec.SecurityContext.SupplementalGroups,
		"the Job must hold its own copy of the configured groups")
}

// §11: the worker gets the Deployment's UMASK.
func TestBuildJobPassesUmask(t *testing.T) {
	env := func(c Config) map[string]string {
		out := map[string]string{}
		for _, e := range Template(profile(), transcodev1alpha1.HardwareCPU, c).Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	assert.Equal(t, "002", env(Config{Umask: "002"})[UmaskEnv])
	_, set := env(Config{})[UmaskEnv]
	assert.False(t, set, "no UMASK is invented when the controller has none")
}

// A pool pod's CLUSTARR_CPU_LIMIT is the pool size its plan was made with:
// the Downward API's limits.cpu (rounded up) when the container has a CPU
// limit, and otherwise a literal -- the CPU request rounded up, else the
// default profile's 8 cores -- never the node's allocatable CPU, which the
// Downward API would report and no plan can know.
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
			require.Equal(t, tc.threads, Threads(p))
			ctr := Template(p, transcodev1alpha1.HardwareCPU, Config{Image: "cpu:1"}).Spec.Containers[0]
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

func TestTemplatePodsRunWithoutAServiceAccountToken(t *testing.T) {
	for _, class := range []transcodev1alpha1.Hardware{"cpu", "nvidia", "intel"} {
		pod := Template(profile(), class, cfg).Spec
		assert.False(t, *pod.AutomountServiceAccountToken, class)
		assert.Empty(t, pod.ServiceAccountName, class)
		assert.Equal(t, []string{"--data-dir", "/data"}, pod.Containers[0].Args[:2])
	}
}
