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
package guards

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/test/installtest"
)

// TestChartIndexCardigannReachesIndex pins what the chart renders for
// manager.cardigann.* (X16 verified it with `helm template` by hand only;
// the values moved from index to the manager, whose bundle loader applies
// the definitions, in the installers wave): each value's env var, volume and
// mount on the manager Deployment, so a renamed env var, volume or key fails
// here rather than on a cluster. The default render adds nothing -- the
// binary's own defaults (bundled on, no directory) are what an unconfigured
// chart means. That the variables reach --cardigann-bundled and
// --cardigann-definitions-dir is cmd/manager's own test (W5.7); the
// command-tree half this test had parsed the retired clustarr binary.
func TestChartIndexCardigannReachesIndex(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	const dir = "/etc/clustarr/cardigann"
	for name, tc := range map[string]struct {
		args   []string
		env    map[string]string // the cardigann env vars rendered, exactly
		volume *corev1.VolumeSource
		mount  *corev1.VolumeMount
	}{
		"defaults": {env: map[string]string{}},
		"bundled off": {
			args: []string{"--set", "manager.cardigann.bundled=false"},
			env:  map[string]string{cli.EnvCardigannBundled: "false"},
		},
		"configMap": {
			args:   []string{"--set", "manager.cardigann.definitions.configMap=cardigann-defs"},
			env:    map[string]string{cli.EnvCardigannDefinitionsDir: dir},
			volume: &corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cardigann-defs"}}},
			mount:  &corev1.VolumeMount{Name: "cardigann", MountPath: dir, ReadOnly: true},
		},
		"existingClaim with subPath": {
			args: []string{
				"--set", "manager.cardigann.definitions.existingClaim=cardigann-pvc",
				"--set", "manager.cardigann.definitions.subPath=v11",
			},
			env:    map[string]string{cli.EnvCardigannDefinitionsDir: dir},
			volume: &corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "cardigann-pvc", ReadOnly: true}},
			mount:  &corev1.VolumeMount{Name: "cardigann", MountPath: dir, ReadOnly: true, SubPath: "v11"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := installtest.Run(t, root, helm, append([]string{"template", "clustarr", "charts/clustarr"}, tc.args...)...)
			dep := managerDeployment(t, installtest.DecodeRendered(t, out).Deployments)
			ctr := dep.Spec.Template.Spec.Containers[0]

			got := map[string]string{}
			for _, e := range ctr.Env {
				if e.Name == cli.EnvCardigannBundled || e.Name == cli.EnvCardigannDefinitionsDir {
					got[e.Name] = e.Value
				}
			}
			require.Equal(t, tc.env, got, "the cardigann env vars on the manager container")

			var vol *corev1.Volume
			for i := range dep.Spec.Template.Spec.Volumes {
				if dep.Spec.Template.Spec.Volumes[i].Name == "cardigann" {
					vol = &dep.Spec.Template.Spec.Volumes[i]
				}
			}
			var mount *corev1.VolumeMount
			for i := range ctr.VolumeMounts {
				if ctr.VolumeMounts[i].Name == "cardigann" {
					mount = &ctr.VolumeMounts[i]
				}
			}
			if tc.volume == nil {
				require.Nil(t, vol, "no definitions source, no cardigann volume")
				require.Nil(t, mount)
			} else {
				require.NotNil(t, vol)
				require.Equal(t, *tc.volume, vol.VolumeSource)
				require.NotNil(t, mount)
				require.Equal(t, *tc.mount, *mount)
			}
		})
	}
}

// TestChartRefusesTwoCardigannDefinitionSources: the manager loads one
// definitions directory, so a configMap and an existingClaim together fail
// the render (clustarr.validate) instead of mounting one and ignoring the
// other.
func TestChartRefusesTwoCardigannDefinitionSources(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	cmd := exec.Command(helm, "template", "clustarr", "charts/clustarr",
		"--set", "manager.cardigann.definitions.configMap=a",
		"--set", "manager.cardigann.definitions.existingClaim=b")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.Error(t, cmd.Run(), "a render with both definition sources must fail")
	require.Contains(t, stderr.String(), "manager.cardigann.definitions: set configMap or existingClaim, not both")
}

func managerDeployment(t *testing.T, deps []appsv1.Deployment) *appsv1.Deployment {
	t.Helper()
	for i := range deps {
		if deps[i].Spec.Template.Labels["app.kubernetes.io/component"] == "manager" {
			return &deps[i]
		}
	}
	t.Fatal("no manager Deployment rendered")
	return nil
}
