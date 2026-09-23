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

package downloadclient

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/yaml"
)

// enginePods renders both engine kinds' pod specs as the apiserver would
// receive them, through JSON, so the assertions read native types.
func enginePods(t *testing.T) map[string]corev1.PodSpec {
	t.Helper()
	native := func(ac *corev1ac.PodSpecApplyConfiguration) corev1.PodSpec {
		raw, err := json.Marshal(ac)
		require.NoError(t, err)
		var spec corev1.PodSpec
		require.NoError(t, json.Unmarshal(raw, &spec))
		return spec
	}
	sts := buildStatefulSet(torrentClient("qbit", 1), "qbit-engine", "img", "/data", "clustarr-data", EngineRuntime{}, fakeOwnerRef())
	dep := buildDeployment(usenetClient("nzb"), "nzb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, fakeOwnerRef())
	return map[string]corev1.PodSpec{
		"torrent": native(sts.Spec.Template.Spec),
		"usenet":  native(dep.Spec.Template.Spec),
	}
}

// Every engine pod runs with the security settings every Deployment has:
// non-root as the image's clustarr user, RuntimeDefault seccomp, fsGroup on
// its volumes, no capabilities, no privilege escalation, and a read-only root
// filesystem -- with every path the engine writes a volume, /tmp included.
// Until X16 an engine pod carried none of it.
func TestEnginePodSecurity(t *testing.T) {
	writes := map[string][]string{
		"torrent": {"/data", "/tmp"},
		"usenet":  {"/data", "/scratch", "/tmp"},
	}
	for kind, pod := range enginePods(t) {
		t.Run(kind, func(t *testing.T) {
			psc := pod.SecurityContext
			require.NotNil(t, psc, "the pod has no securityContext")
			require.NotNil(t, psc.RunAsNonRoot)
			assert.True(t, *psc.RunAsNonRoot)
			require.NotNil(t, psc.RunAsUser)
			assert.Equal(t, int64(1000), *psc.RunAsUser)
			require.NotNil(t, psc.RunAsGroup)
			assert.Equal(t, int64(1000), *psc.RunAsGroup)
			require.NotNil(t, psc.FSGroup)
			assert.Equal(t, int64(1000), *psc.FSGroup)
			require.NotNil(t, psc.FSGroupChangePolicy)
			assert.Equal(t, corev1.FSGroupChangeOnRootMismatch, *psc.FSGroupChangePolicy)
			require.NotNil(t, psc.SeccompProfile)
			assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, psc.SeccompProfile.Type)

			require.Len(t, pod.Containers, 1)
			csc := pod.Containers[0].SecurityContext
			require.NotNil(t, csc, "the container has no securityContext")
			require.NotNil(t, csc.ReadOnlyRootFilesystem)
			assert.True(t, *csc.ReadOnlyRootFilesystem)
			require.NotNil(t, csc.AllowPrivilegeEscalation)
			assert.False(t, *csc.AllowPrivilegeEscalation)
			require.NotNil(t, csc.Capabilities)
			assert.Equal(t, []corev1.Capability{"ALL"}, csc.Capabilities.Drop)
			assert.Empty(t, csc.Capabilities.Add)

			mounted := map[string]string{}
			for _, m := range pod.Containers[0].VolumeMounts {
				mounted[m.MountPath] = m.Name
			}
			volumes := map[string]corev1.Volume{}
			for _, v := range pod.Volumes {
				volumes[v.Name] = v
			}
			for _, dir := range writes[kind] {
				name, ok := mounted[dir]
				require.True(t, ok, "%s is written by the %s engine but is not a volume: the read-only root refuses it", dir, kind)
				_, ok = volumes[name]
				assert.True(t, ok, "%s mounts volume %q, which the pod does not declare", dir, name)
			}
			require.NotNil(t, volumes[tmpVolumeName].EmptyDir, "/tmp is an emptyDir")
		})
	}
}

// "Matching the Deployments" is a claim about config/manager, so read it:
// each engine pod's two security contexts must equal grabarr's own
// Deployment's, the controller that creates them.
func TestEnginePodSecurityMatchesTheDeployment(t *testing.T) {
	raw, err := os.ReadFile("../../../config/manager/grabarr.yaml")
	require.NoError(t, err)
	var deploy *appsv1.Deployment
	for _, doc := range strings.Split(string(raw), "\n---") {
		var d appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &d) == nil && d.Kind == "Deployment" {
			deploy = &d
		}
	}
	require.NotNil(t, deploy, "config/manager/grabarr.yaml has no Deployment")
	want := deploy.Spec.Template.Spec

	for kind, pod := range enginePods(t) {
		assert.True(t, equality.Semantic.DeepEqual(want.SecurityContext, pod.SecurityContext),
			"%s engine pod securityContext\n engine:     %+v\n deployment: %+v", kind, pod.SecurityContext, want.SecurityContext)
		assert.True(t, equality.Semantic.DeepEqual(want.Containers[0].SecurityContext, pod.Containers[0].SecurityContext),
			"%s engine container securityContext\n engine:     %+v\n deployment: %+v",
			kind, pod.Containers[0].SecurityContext, want.Containers[0].SecurityContext)
	}
}
