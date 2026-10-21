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
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	placementpkg "github.com/mediactl/clustarr/pkg/placement"
	"github.com/mediactl/clustarr/test/installtest"
)

// The control-plane placement design's profiles (§3), verbatim.
var (
	controlPlaneExpr = corev1.NodeSelectorRequirement{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpExists}
	workersExprs     = []corev1.NodeSelectorRequirement{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist},
		{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64"}},
	}
	controlPlaneToleration = corev1.Toleration{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
)

// placementOn turns placement on with the subcharts' literal placements the
// render checks (NATS on the control plane, nats-box on the workers).
func placementOn(extra ...string) []string {
	return append([]string{"--set", "placement.enabled=true",
		"--set-json", "nats.podTemplate.merge.spec.affinity=" + cpAffinity,
		"--set-json", "nats.natsBox.podTemplate.merge.spec.affinity=" + workersAffinity}, extra...)
}

// renderErr is RenderChart for a render that may fail: its stderr, and
// whether helm exited non-zero.
func renderErr(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(installtest.FindTool(t, "helm"), append([]string{"template", "clustarr", "charts/clustarr"}, args...)...)
	cmd.Dir = installtest.RepoRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err != nil
}

func requiredTerms(t *testing.T, spec corev1.PodSpec) []corev1.NodeSelectorTerm {
	t.Helper()
	require.NotNil(t, spec.Affinity, "no affinity")
	require.NotNil(t, spec.Affinity.NodeAffinity, "no node affinity")
	require.NotNil(t, spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution, "no required node affinity")
	return spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
}

// pdbs is clustarr's own PodDisruptionBudgets by component (the nats
// subchart renders one of its own).
func pdbs(t *testing.T, out []byte) map[string]map[string]any {
	t.Helper()
	got := map[string]map[string]any{}
	for _, o := range installtest.Objects(t, out) {
		if o.GetKind() == "PodDisruptionBudget" && o.GetLabels()["app.kubernetes.io/name"] == "clustarr" {
			got[o.GetLabels()["app.kubernetes.io/component"]] = o.Object["spec"].(map[string]any)
		}
	}
	return got
}

// TestPlacementOffLeavesTheDefaultRenderAlone: placement is off by default,
// and a default render carries none of it (the plan's Global Constraints).
func TestPlacementOffLeavesTheDefaultRenderAlone(t *testing.T) {
	out := installtest.RenderChart(t)
	assert.NotContains(t, string(out), "node-role.kubernetes.io/control-plane")
	assert.Empty(t, pdbs(t, out))
	w := installtest.Workloads(installtest.DecodeRendered(t, out))
	m := w["manager"]
	require.NotNil(t, m.Spec.Replicas)
	assert.Equal(t, int32(1), *m.Spec.Replicas)
	assert.Equal(t, "1", m.Spec.Strategy.RollingUpdate.MaxSurge.String())
	assert.Equal(t, "0", m.Spec.Strategy.RollingUpdate.MaxUnavailable.String())
	for _, a := range m.Spec.Template.Spec.Containers[0].Args {
		assert.NotContains(t, a, "--worker-placement")
	}
	ui := w["ui"]
	require.NotNil(t, ui.Spec.Replicas)
	assert.Equal(t, int32(1), *ui.Spec.Replicas)
}

// TestPlacementProfilesRenderPerComponent is the design's §3 and §4: the
// manager and the ui three to a control-plane node each, everything else on
// the workers, and the manager handed the workers profile for the pods it
// builds (§5.6).
func TestPlacementProfilesRenderPerComponent(t *testing.T) {
	out := installtest.RenderChart(t, placementOn(
		"--set-json", `markers.affinity={"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"intel.feature.node.kubernetes.io/gpu","operator":"DoesNotExist"}]}]}}}`)...)
	w := installtest.Workloads(installtest.DecodeRendered(t, out))
	budgets := pdbs(t, out)

	for _, c := range []string{"manager", "ui"} {
		t.Run(c, func(t *testing.T) {
			d := w[c]
			require.NotNil(t, d.Spec.Replicas)
			assert.Equal(t, int32(3), *d.Spec.Replicas)
			require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, d.Spec.Strategy.Type)
			assert.Equal(t, "0", d.Spec.Strategy.RollingUpdate.MaxSurge.String(), "a surge pod has nowhere to go")
			assert.Equal(t, "1", d.Spec.Strategy.RollingUpdate.MaxUnavailable.String())
			spec := d.Spec.Template.Spec
			for _, term := range requiredTerms(t, spec) {
				assert.Contains(t, term.MatchExpressions, controlPlaneExpr)
			}
			assert.Contains(t, spec.Tolerations, controlPlaneToleration)
			require.NotNil(t, spec.Affinity.PodAntiAffinity)
			anti := spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			require.Len(t, anti, 1)
			assert.Equal(t, "kubernetes.io/hostname", anti[0].TopologyKey)
			assert.Equal(t, c, anti[0].LabelSelector.MatchLabels["app.kubernetes.io/component"])
			for k, v := range anti[0].LabelSelector.MatchLabels {
				assert.Equal(t, v, d.Spec.Template.Labels[k], "the anti-affinity selects the component's own pods")
			}
			require.Contains(t, budgets, c)
			assert.EqualValues(t, 1, budgets[c]["maxUnavailable"])
		})
	}
	for name, d := range w {
		if name == "manager" || name == "ui" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, term := range requiredTerms(t, d.Spec.Template.Spec) {
				for _, want := range workersExprs {
					assert.Contains(t, term.MatchExpressions, want)
				}
			}
			assert.NotContains(t, budgets, name)
		})
	}
	markers := requiredTerms(t, w["markers"].Spec.Template.Spec)
	require.Len(t, markers, 1, "the workers profile joins markers' own term")
	assert.Equal(t, append([]corev1.NodeSelectorRequirement{
		{Key: "intel.feature.node.kubernetes.io/gpu", Operator: corev1.NodeSelectorOpDoesNotExist},
	}, workersExprs...), markers[0].MatchExpressions)

	var placement string
	for _, a := range w["manager"].Spec.Template.Spec.Containers[0].Args {
		if v, ok := strings.CutPrefix(a, "--worker-placement="); ok {
			placement = v
		}
	}
	require.NotEmpty(t, placement, "the manager is handed the workers profile")
	var got struct {
		NodeAffinity []corev1.NodeSelectorRequirement `json:"nodeAffinity"`
		Tolerations  []corev1.Toleration              `json:"tolerations"`
	}
	require.NoError(t, json.Unmarshal([]byte(placement), &got))
	_, err := placementpkg.Parse(placement)
	require.NoError(t, err, "the manager parses the chart's --worker-placement strictly")
	assert.Equal(t, workersExprs, got.NodeAffinity)
	assert.Empty(t, got.Tolerations)
}

// TestReplicasAboveTheProfileFailTheRender: more replicas than control-plane
// nodes would leave a pod Pending for good (the plan's Review Focus 5).
func TestReplicasAboveTheProfileFailTheRender(t *testing.T) {
	stderr, failed := renderErr(t, placementOn("--set", "manager.replicas=4")...)
	require.True(t, failed, "a render with four manager replicas on a three-node profile succeeded")
	assert.Contains(t, stderr, "manager.replicas")
}

// TestFewerReplicasThanTheProfileAreKept: a component may run fewer.
func TestFewerReplicasThanTheProfileAreKept(t *testing.T) {
	out := installtest.RenderChart(t, placementOn("--set", "ui.replicas=2")...)
	ui := installtest.Workloads(installtest.DecodeRendered(t, out))["ui"]
	require.NotNil(t, ui.Spec.Replicas)
	assert.Equal(t, int32(2), *ui.Spec.Replicas)
}

// TestMultiTermAffinityGetsTheProfileInEveryTerm: terms OR, so the profile
// joins every one of them (the plan's Review Focus 1).
func TestMultiTermAffinityGetsTheProfileInEveryTerm(t *testing.T) {
	out := installtest.RenderChart(t, placementOn(
		"--set-json", `ui.affinity={"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"a","operator":"Exists"}]},{"matchExpressions":[{"key":"b","operator":"Exists"}]}]}}}`)...)
	terms := requiredTerms(t, installtest.Workloads(installtest.DecodeRendered(t, out))["ui"].Spec.Template.Spec)
	require.Len(t, terms, 2)
	for _, term := range terms {
		assert.Contains(t, term.MatchExpressions, controlPlaneExpr)
	}
}

const (
	cpAffinity      = `{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists"}]}]}}}`
	workersAffinity = `{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"DoesNotExist"},{"key":"kubernetes.io/arch","operator":"In","values":["amd64"]}]}]}}}`
)

// TestNATSPlacementMatchesTheControlPlaneProfile: the nats subchart takes
// its placement literally (it cannot call the parent's helpers), so the
// render checks the copy (control-plane placement design §5.2).
func TestNATSPlacementMatchesTheControlPlaneProfile(t *testing.T) {
	stderr, failed := renderErr(t, "--set", "placement.enabled=true",
		"--set-json", "nats.natsBox.podTemplate.merge.spec.affinity="+workersAffinity)
	require.True(t, failed, "NATS rendered with no control-plane placement")
	assert.Contains(t, stderr, "nats.podTemplate.merge.spec.affinity")

	_, failed = renderErr(t, "--set", "placement.enabled=true",
		"--set-json", "nats.podTemplate.merge.spec.affinity="+cpAffinity,
		"--set-json", "nats.natsBox.podTemplate.merge.spec.affinity="+workersAffinity)
	assert.False(t, failed, "a control-plane NATS placement renders")
}

// TestNatsBoxOnWorkers: nats-box is everything else, on the workers (§5.5).
func TestNatsBoxOnWorkers(t *testing.T) {
	stderr, failed := renderErr(t, "--set", "placement.enabled=true",
		"--set-json", "nats.podTemplate.merge.spec.affinity="+cpAffinity)
	require.True(t, failed, "nats-box rendered with no workers placement")
	assert.Contains(t, stderr, "nats.natsBox.podTemplate.merge.spec.affinity")
}

// TestLoadBalancerWorkloadsStayOnTheControlPlane is the owner's rule
// (2026-10-21): a workload behind a LoadBalancer Service -- Plex, whose
// cluster-plex Service always is one -- runs on the control plane, where
// the BGP peers are.
func TestLoadBalancerWorkloadsStayOnTheControlPlane(t *testing.T) {
	base := placementOn("--set", "cluster-plex.enabled=true")
	stderr, failed := renderErr(t, base...)
	require.True(t, failed, "Plex rendered with no control-plane placement")
	assert.Contains(t, stderr, "cluster-plex.plex.affinity")

	_, failed = renderErr(t, append(base, "--set-json", "cluster-plex.plex.affinity="+cpAffinity)...)
	assert.False(t, failed)
}

// TestPlacementChecksNeedPlacementEnabled: with placement off nothing is
// checked, so a kind or single-node install renders as before.
func TestPlacementChecksNeedPlacementEnabled(t *testing.T) {
	_, failed := renderErr(t, "--set", "cluster-plex.enabled=true")
	assert.False(t, failed)
}

// TestTheProxyBehindALoadBalancerStaysOnTheControlPlane is the final
// review's I2: cluster-plex's proxy has its own LoadBalancer Service
// (externalTrafficPolicy Local), so with the proxy on it is held to the
// control plane like Plex.
func TestTheProxyBehindALoadBalancerStaysOnTheControlPlane(t *testing.T) {
	base := placementOn("--set", "cluster-plex.enabled=true", "--set-json", "cluster-plex.plex.affinity="+cpAffinity,
		"--set", "cluster-plex.proxy.enabled=true")
	stderr, failed := renderErr(t, base...)
	require.True(t, failed, "the proxy rendered with no control-plane placement")
	assert.Contains(t, stderr, "cluster-plex.proxy.affinity")

	_, failed = renderErr(t, append(base, "--set-json", "cluster-plex.proxy.affinity="+cpAffinity)...)
	assert.False(t, failed)
}
