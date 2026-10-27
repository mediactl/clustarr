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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/mediactl/clustarr/test/installtest"
)

// uiHPA is the rendered HorizontalPodAutoscaler named clustarr-ui, or nil.
func uiHPA(t *testing.T, out []byte) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	for _, o := range installtest.Objects(t, out) {
		if o.GetKind() != "HorizontalPodAutoscaler" || o.GetName() != "clustarr-ui" {
			continue
		}
		var hpa autoscalingv2.HorizontalPodAutoscaler
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &hpa))
		return &hpa
	}
	return nil
}

// uiRoute is the rendered HTTPRoute named clustarr-ui, or nil.
func uiRoute(t *testing.T, out []byte) *unstructured.Unstructured {
	t.Helper()
	for _, o := range installtest.Objects(t, out) {
		if o.GetKind() == "HTTPRoute" && o.GetName() == "clustarr-ui" {
			return o
		}
	}
	return nil
}

func uiAntiAffinityTerms(d appsv1.Deployment) int {
	a := d.Spec.Template.Spec.Affinity
	if a == nil || a.PodAntiAffinity == nil {
		return 0
	}
	return len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
}

// TestUIAutoscalingIsOptIn: by default the ui is a fixed Deployment with no
// HPA and no route, as every release before had it.
func TestUIAutoscalingIsOptIn(t *testing.T) {
	out := installtest.RenderChart(t)
	assert.Nil(t, uiHPA(t, out))
	assert.Nil(t, uiRoute(t, out))
	ui := installtest.Workloads(installtest.DecodeRendered(t, out))["ui"]
	require.NotNil(t, ui.Spec.Replicas)
	assert.Equal(t, int32(1), *ui.Spec.Replicas)
	assert.Zero(t, uiAntiAffinityTerms(ui))
}

// TestUIAutoscalingRendersAnHPAThatOwnsTheCount: with ui.autoscaling the
// chart renders Helm's own HPA on CPU between min and max, the Deployment
// carries no spec.replicas (an upgrade would reset the HPA's count), its
// pods spread one per node by a required hostname anti-affinity, and the
// rollout surges one and takes one down at a time, since at maxReplicas a
// surge pod has no node until an old pod frees one.
func TestUIAutoscalingRendersAnHPAThatOwnsTheCount(t *testing.T) {
	out := installtest.RenderChart(t, "--set", "ui.autoscaling.enabled=true",
		"--set", "ui.autoscaling.minReplicas=1", "--set", "ui.autoscaling.maxReplicas=3",
		"--set", "ui.autoscaling.targetCPUUtilizationPercentage=60")
	hpa := uiHPA(t, out)
	require.NotNil(t, hpa, "no HPA clustarr-ui rendered")
	assert.Equal(t, "Deployment", hpa.Spec.ScaleTargetRef.Kind)
	assert.Equal(t, "clustarr-ui", hpa.Spec.ScaleTargetRef.Name)
	require.NotNil(t, hpa.Spec.MinReplicas)
	assert.Equal(t, int32(1), *hpa.Spec.MinReplicas)
	assert.Equal(t, int32(3), hpa.Spec.MaxReplicas)
	require.Len(t, hpa.Spec.Metrics, 1)
	require.NotNil(t, hpa.Spec.Metrics[0].Resource)
	assert.Equal(t, "cpu", string(hpa.Spec.Metrics[0].Resource.Name))
	require.NotNil(t, hpa.Spec.Metrics[0].Resource.Target.AverageUtilization)
	assert.Equal(t, int32(60), *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization)
	assert.Empty(t, hpa.Labels["autoscale.clustarr.io/domain"], "the manager's reconciler labels the HPAs it owns; this one is Helm's")

	ui := installtest.Workloads(installtest.DecodeRendered(t, out))["ui"]
	assert.Nil(t, ui.Spec.Replicas, "the HPA owns the count")
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, ui.Spec.Strategy.Type)
	assert.Equal(t, "1", ui.Spec.Strategy.RollingUpdate.MaxSurge.String())
	assert.Equal(t, "1", ui.Spec.Strategy.RollingUpdate.MaxUnavailable.String())
	require.Equal(t, 1, uiAntiAffinityTerms(ui))
	term := ui.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, "kubernetes.io/hostname", term.TopologyKey)
	assert.Equal(t, "ui", term.LabelSelector.MatchLabels["app.kubernetes.io/component"])
}

// TestUIReplicasSpreadOnePerNode: a fixed ui.replicas above 1 spreads the
// same way, HPA or not -- the required hostname anti-affinity and the
// surge-one, take-one-down rollout -- and a single replica takes neither,
// since on a one-node cluster its surge pod would never schedule.
func TestUIReplicasSpreadOnePerNode(t *testing.T) {
	out := installtest.RenderChart(t, "--set", "ui.replicas=2")
	ui := installtest.Workloads(installtest.DecodeRendered(t, out))["ui"]
	require.NotNil(t, ui.Spec.Replicas)
	assert.Equal(t, int32(2), *ui.Spec.Replicas)
	require.Equal(t, 1, uiAntiAffinityTerms(ui))
	term := ui.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, "kubernetes.io/hostname", term.TopologyKey)
	assert.Equal(t, "ui", term.LabelSelector.MatchLabels["app.kubernetes.io/component"])
	assert.Equal(t, "1", ui.Spec.Strategy.RollingUpdate.MaxSurge.String())
	assert.Equal(t, "1", ui.Spec.Strategy.RollingUpdate.MaxUnavailable.String())
	assert.Nil(t, uiHPA(t, out))

	one := installtest.Workloads(installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "ui.replicas=1")))["ui"]
	assert.Zero(t, uiAntiAffinityTerms(one))
	assert.Nil(t, one.Spec.Strategy.RollingUpdate)
}

// TestUIAutoscalingUnderAOnePerNodeProfile: the profile's anti-affinity
// is the one rendered (never two terms), its node affinity and toleration
// apply, the Deployment keeps the profile's maxSurge 0, and the HPA may
// not ask for more pods than the profile has nodes.
func TestUIAutoscalingUnderAOnePerNodeProfile(t *testing.T) {
	out := installtest.RenderChart(t, placementOn("--set", "ui.autoscaling.enabled=true")...)
	ui := installtest.Workloads(installtest.DecodeRendered(t, out))["ui"]
	assert.Nil(t, ui.Spec.Replicas)
	assert.Equal(t, "0", ui.Spec.Strategy.RollingUpdate.MaxSurge.String())
	assert.Equal(t, 1, uiAntiAffinityTerms(ui))
	for _, term := range requiredTerms(t, ui.Spec.Template.Spec) {
		assert.Contains(t, term.MatchExpressions, controlPlaneExpr)
	}
	assert.Contains(t, ui.Spec.Template.Spec.Tolerations, controlPlaneToleration)
	require.NotNil(t, uiHPA(t, out))

	stderr, failed := renderErr(t, placementOn("--set", "ui.autoscaling.enabled=true", "--set", "ui.autoscaling.maxReplicas=4")...)
	require.True(t, failed, "four replicas on a three-node profile rendered")
	assert.Contains(t, stderr, "ui.autoscaling.maxReplicas")
}

// TestUIAutoscalingRefusesAnAmbiguousCount: a fixed ui.replicas beside the
// HPA, a minimum of zero, a minimum above the maximum and a ui with no CPU
// request (nothing for utilization to be a share of) all fail the render.
func TestUIAutoscalingRefusesAnAmbiguousCount(t *testing.T) {
	for name, args := range map[string][]string{
		"replicas beside the HPA": {"--set", "ui.replicas=2"},
		"minReplicas 0":           {"--set", "ui.autoscaling.minReplicas=0"},
		"min above max":           {"--set", "ui.autoscaling.minReplicas=4"},
		"no cpu request":          {"--set", "ui.resources.requests.cpu=null"},
	} {
		t.Run(name, func(t *testing.T) {
			_, failed := renderErr(t, append([]string{"--set", "ui.autoscaling.enabled=true"}, args...)...)
			assert.True(t, failed)
		})
	}
}

// TestUIHTTPRouteIsOptIn: ui.httpRoute renders one HTTPRoute to the ui's
// Service port, with the parentRefs and hostnames as given, and refuses a
// route with no parent.
func TestUIHTTPRouteIsOptIn(t *testing.T) {
	out := installtest.RenderChart(t, "--set", "ui.httpRoute.enabled=true",
		"--set-json", `ui.httpRoute.parentRefs=[{"name":"cilium","namespace":"gateway","sectionName":"https-internal"}]`,
		"--set-json", `ui.httpRoute.hostnames=["clustarr.k8s.example.com"]`,
		"--set", "ui.service.port=9090")
	r := uiRoute(t, out)
	require.NotNil(t, r, "no HTTPRoute clustarr-ui rendered")
	assert.Equal(t, "gateway.networking.k8s.io/v1", r.GetAPIVersion())
	parents, _, _ := unstructured.NestedSlice(r.Object, "spec", "parentRefs")
	assert.Equal(t, []any{map[string]any{"name": "cilium", "namespace": "gateway", "sectionName": "https-internal"}}, parents)
	hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames")
	assert.Equal(t, []string{"clustarr.k8s.example.com"}, hosts)
	rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
	require.Len(t, rules, 1)
	backends, _ := rules[0].(map[string]any)["backendRefs"].([]any)
	require.Len(t, backends, 1)
	assert.Equal(t, "clustarr-ui", backends[0].(map[string]any)["name"])
	assert.EqualValues(t, 9090, backends[0].(map[string]any)["port"])

	stderr, failed := renderErr(t, "--set", "ui.httpRoute.enabled=true")
	require.True(t, failed, "a route with no parentRefs rendered")
	assert.Contains(t, stderr, "ui.httpRoute.parentRefs")
}
