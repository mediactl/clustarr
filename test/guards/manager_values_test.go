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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/test/installtest"
)

// TestCardigannValuesReachTheManager: manager.cardigann.* render the
// manager's env, volume and read-only mount (the bundle loader is the
// manager's, R9); cmd/manager's manifest test parses them. The default
// render adds nothing: the binary's defaults are bundled on, no directory.
// The index agent loads no bundle at all.
func TestCardigannValuesReachTheManager(t *testing.T) {
	const dir = "/etc/clustarr/cardigann"
	for name, tc := range map[string]struct {
		args   []string
		env    map[string]string
		volume *corev1.VolumeSource
		mount  *corev1.VolumeMount
	}{
		"defaults":    {env: map[string]string{}},
		"bundled off": {args: []string{"--set", "manager.cardigann.bundled=false"}, env: map[string]string{cli.EnvCardigannBundled: "false"}},
		"configMap": {
			args:   []string{"--set", "manager.cardigann.definitions.configMap=cardigann-defs"},
			env:    map[string]string{cli.EnvCardigannDefinitionsDir: dir},
			volume: &corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cardigann-defs"}}},
			mount:  &corev1.VolumeMount{Name: "cardigann", MountPath: dir, ReadOnly: true},
		},
		"existingClaim with subPath": {
			args:   []string{"--set", "manager.cardigann.definitions.existingClaim=cardigann-pvc", "--set", "manager.cardigann.definitions.subPath=v11"},
			env:    map[string]string{cli.EnvCardigannDefinitionsDir: dir},
			volume: &corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "cardigann-pvc", ReadOnly: true}},
			mount:  &corev1.VolumeMount{Name: "cardigann", MountPath: dir, ReadOnly: true, SubPath: "v11"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, ok := installtest.Workloads(installtest.DecodeRendered(t, installtest.RenderChart(t, tc.args...)))["manager"]
			require.True(t, ok, "no manager Deployment")
			c := d.Spec.Template.Spec.Containers[0]
			got := map[string]string{}
			for _, e := range c.Env {
				if strings.HasPrefix(e.Name, "CLUSTARR_CARDIGANN_") {
					got[e.Name] = e.Value
				}
			}
			require.Equal(t, tc.env, got, "the cardigann env vars on the manager")
			var vol *corev1.Volume
			for i := range d.Spec.Template.Spec.Volumes {
				if d.Spec.Template.Spec.Volumes[i].Name == "cardigann" {
					vol = &d.Spec.Template.Spec.Volumes[i]
				}
			}
			var mount *corev1.VolumeMount
			for i := range c.VolumeMounts {
				if c.VolumeMounts[i].Name == "cardigann" {
					mount = &c.VolumeMounts[i]
				}
			}
			if tc.volume == nil {
				require.Nil(t, vol)
				require.Nil(t, mount)
				return
			}
			require.NotNil(t, vol)
			require.Equal(t, *tc.volume, vol.VolumeSource)
			require.NotNil(t, mount)
			require.Equal(t, *tc.mount, *mount)
		})
	}
	off := installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "manager.cardigann.bundled=false"))
	for _, e := range installtest.Container(t, off, "agent-index").Env {
		assert.False(t, strings.HasPrefix(e.Name, "CLUSTARR_CARDIGANN_"), "the index agent loads no Cardigann bundle (R8, R9): %s", e.Name)
	}
}

// TestChartRefusesTwoCardigannDefinitionSources: the manager loads one
// definitions directory, so a configMap and an existingClaim together fail
// the render (clustarr.validate) instead of mounting one and ignoring the
// other.
func TestChartRefusesTwoCardigannDefinitionSources(t *testing.T) {
	cmd := exec.Command(installtest.FindTool(t, "helm"), "template", "clustarr", "charts/clustarr",
		"--set", "manager.cardigann.definitions.configMap=a",
		"--set", "manager.cardigann.definitions.existingClaim=b")
	cmd.Dir = installtest.RepoRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.Error(t, cmd.Run(), "a render with both definition sources must fail")
	require.Contains(t, stderr.String(), "manager.cardigann.definitions: set configMap or existingClaim, not both")
	// Each alone renders.
	installtest.RenderChart(t, "--set", "manager.cardigann.definitions.configMap=a")
	installtest.RenderChart(t, "--set", "manager.cardigann.definitions.existingClaim=b")
}
