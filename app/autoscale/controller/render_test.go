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

package controller_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/app/autoscale/controller"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

type depOpt func(*appsv1.Deployment)

func dep(domain string, opts ...depOpt) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-" + domain, Namespace: "clustarr-system", UID: "uid-1",
			Labels: map[string]string{agentdomain.LabelDomain: domain, "app.kubernetes.io/component": "agent-" + domain},
		},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent"}}},
		}},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

func slotsEnv(v string) depOpt {
	return func(d *appsv1.Deployment) {
		c := &d.Spec.Template.Spec.Containers[0]
		c.Env = append(c.Env, corev1.EnvVar{Name: events.SlotsEnv, Value: v})
	}
}

func slotsFromConfigMap(d *appsv1.Deployment) {
	c := &d.Spec.Template.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: events.SlotsEnv, ValueFrom: &corev1.EnvVarSource{
		ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "slots"}, Key: "slots"},
	}})
}

func disagreeingContainers(d *appsv1.Deployment) {
	d.Spec.Template.Spec.Containers = []corev1.Container{
		{Name: "a", Env: []corev1.EnvVar{{Name: events.SlotsEnv, Value: events.ConsumerImportScan + "=2"}}},
		{Name: "b", Env: []corev1.EnvVar{{Name: events.SlotsEnv, Value: events.ConsumerImportScan + "=3"}}},
	}
}

func minReplicas(v string) depOpt {
	return func(d *appsv1.Deployment) {
		d.Annotations = map[string]string{agentdomain.AnnotationMinReplicas: v}
	}
}

func input(t *testing.T, d *appsv1.Deployment, nodes int) controller.RenderInput {
	t.Helper()
	dom, ok := agentdomain.Lookup(d.Labels[agentdomain.LabelDomain])
	require.True(t, ok)
	var cs []events.ConsumerSpec
	for _, name := range dom.Consumers {
		c, ok := events.Default().Consumer(name)
		require.True(t, ok, name)
		cs = append(cs, c)
	}
	return controller.RenderInput{Deployment: d, Domain: dom, Consumers: cs, MatchingNodes: nodes}
}

// targets maps each External metric's consumer label to its AverageValue.
func targets(t *testing.T, out controller.Rendered) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, m := range out.HPA.Spec.Metrics {
		ext := m.External
		require.NotNil(t, ext)
		assert.Equal(t, agentdomain.MetricConsumerLag, *ext.Metric.Name)
		got[ext.Metric.Selector.MatchLabels["consumer"]] = ext.Target.AverageValue.String()
	}
	return got
}

func reasons(out controller.Rendered) []string {
	var r []string
	for _, w := range out.Warnings {
		r = append(r, w.Reason)
	}
	return r
}

// The HPA §9.5 prints, for caption on kind-cluster-plex's one node.
func TestRenderHPAGolden(t *testing.T) {
	out := controller.RenderHPA(input(t, dep(agentdomain.Caption), 1))
	raw, err := json.Marshal(out.HPA)
	require.NoError(t, err)
	assert.JSONEq(t, `{
			"kind": "HorizontalPodAutoscaler",
			"apiVersion": "autoscaling/v2",
			"metadata": {
					"name": "agent-caption",
					"namespace": "clustarr-system",
					"labels": {
							"app.kubernetes.io/component": "agent-caption",
							"app.kubernetes.io/part-of": "clustarr",
							"autoscale.clustarr.io/domain": "caption"
					},
					"ownerReferences": [
							{"apiVersion": "apps/v1", "kind": "Deployment", "name": "agent-caption", "uid": "uid-1", "controller": true}
					]
			},
			"spec": {
					"scaleTargetRef": {"apiVersion": "apps/v1", "kind": "Deployment", "name": "agent-caption"},
					"minReplicas": 0,
					"maxReplicas": 1,
					"metrics": [
							{"type": "External", "external": {
									"metric": {"name": "clustarr_consumer_lag", "selector": {"matchLabels": {"consumer": "caption-fetch-high", "stream": "CLUSTARR_WORK_CAPTION"}}},
									"target": {"type": "AverageValue", "averageValue": "16"}}},
							{"type": "External", "external": {
									"metric": {"name": "clustarr_consumer_lag", "selector": {"matchLabels": {"consumer": "caption-fetch-normal", "stream": "CLUSTARR_WORK_CAPTION"}}},
									"target": {"type": "AverageValue", "averageValue": "16"}}}
					],
					"behavior": {
							"scaleUp": {"stabilizationWindowSeconds": 0, "selectPolicy": "Max", "policies": [{"type": "Pods", "value": 1, "periodSeconds": 15}]},
							"scaleDown": {"stabilizationWindowSeconds": 300, "selectPolicy": "Max", "policies": [{"type": "Percent", "value": 100, "periodSeconds": 15}]}
					}
			}
	}`, string(raw))
	assert.Empty(t, out.Warnings)
}

func TestRenderHPA(t *testing.T) {
	importTargets := map[string]string{
		events.ConsumerImportScan: "4", events.ConsumerImportList: "2", events.ConsumerImportFile: "4",
		events.ConsumerImportRecycle: "1", events.ConsumerImportProbeHigh: "4", events.ConsumerImportProbeLow: "4",
	}
	for _, tc := range []struct {
		name        string
		d           *appsv1.Deployment
		nodes       int
		force       bool
		min, max    int32
		targets     map[string]string // a subset to check
		down        int32
		wantReasons []string
	}{
		{name: "import on two nodes", d: dep(agentdomain.Import), nodes: 2, min: 0, max: 2, targets: importTargets, down: 300},
		{name: "markers waits out its 30-minute AckWait before scaling down", d: dep(agentdomain.Markers), nodes: 3,
			min: 0, max: 3, targets: map[string]string{events.ConsumerMarkersAnalyze: "1"}, down: 1800},
		{name: "events never goes below one", d: dep(agentdomain.Events), nodes: 1, min: 1, max: 1, down: 300,
			targets: map[string]string{events.ConsumerCatalogRSSMatcher: "256"}},
		{name: "catalog is throttled to one replica on six nodes", d: dep(agentdomain.Catalog), nodes: 6, min: 0, max: 1, down: 300,
			targets: map[string]string{
				events.ConsumerCatalogSearchHigh: "8", events.ConsumerCatalogSearchNorm: "8",
				events.ConsumerCatalogArtworkRender: "4",
			}},
		{name: "caption is throttled to one replica on six nodes", d: dep(agentdomain.Caption), nodes: 6, min: 0, max: 1, down: 300},
		{name: "a slot override changes the target and the clamp", d: dep(agentdomain.Import, slotsEnv(events.ConsumerImportScan+"=8")), nodes: 6,
			min: 0, max: 4, targets: map[string]string{events.ConsumerImportScan: "8"}, down: 300},
		{name: "an override above MaxAckPending clamps to one", d: dep(agentdomain.Import, slotsEnv(events.ConsumerImportRecycle+"=9")), nodes: 6,
			min: 0, max: 1, targets: map[string]string{events.ConsumerImportRecycle: "9"}, down: 300,
			wantReasons: []string{controller.ReasonSlotsExceedMaxAckPending}},
		{name: "an override naming another domain's consumer is ignored", d: dep(agentdomain.Import, slotsEnv(events.ConsumerCaptionFetchHigh+"=2")),
			nodes: 2, min: 0, max: 2, targets: importTargets, down: 300, wantReasons: []string{controller.ReasonInvalidSlots}},
		{name: "a valueFrom override is ignored", d: dep(agentdomain.Import, slotsFromConfigMap), nodes: 2, min: 0, max: 2,
			targets: importTargets, down: 300, wantReasons: []string{controller.ReasonInvalidSlots}},
		{name: "containers that disagree are ignored", d: dep(agentdomain.Import, disagreeingContainers), nodes: 2, min: 0, max: 2,
			targets: importTargets, down: 300, wantReasons: []string{controller.ReasonInvalidSlots}},
		{name: "a malformed override is ignored", d: dep(agentdomain.Import, slotsEnv(events.ConsumerImportScan)), nodes: 2, min: 0, max: 2,
			targets: importTargets, down: 300, wantReasons: []string{controller.ReasonInvalidSlots}},
		{name: "the min-replicas annotation raises the floor", d: dep(agentdomain.Import, minReplicas("2")), nodes: 6, min: 2, max: 6, down: 300},
		{name: "a min-replicas annotation above capacity raises maxReplicas", d: dep(agentdomain.Import, minReplicas("3")), nodes: 1,
			min: 3, max: 3, down: 300, wantReasons: []string{controller.ReasonMinReplicasAboveCapacity}},
		{name: "a min-replicas annotation never lowers the floor", d: dep(agentdomain.Events, minReplicas("0")), nodes: 2, min: 1, max: 2, down: 300},
		{name: "an unparseable min-replicas annotation is ignored", d: dep(agentdomain.Import, minReplicas("two")), nodes: 2, min: 0, max: 2,
			down: 300, wantReasons: []string{controller.ReasonInvalidMinReplicas}},
		{name: "no matching node", d: dep(agentdomain.Import), nodes: 0, min: 0, max: 1, down: 300,
			wantReasons: []string{controller.ReasonNoMatchingNodes}},
		{name: "ForceMinOne, after ScaleToZeroUnavailable", d: dep(agentdomain.Import), nodes: 2, force: true, min: 1, max: 2, down: 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input(t, tc.d, tc.nodes)
			in.ForceMinOne = tc.force
			out := controller.RenderHPA(in)
			assert.Equal(t, tc.min, out.MinReplicas)
			assert.Equal(t, tc.max, out.MaxReplicas)
			assert.Equal(t, tc.min, *out.HPA.Spec.MinReplicas)
			assert.Equal(t, tc.max, *out.HPA.Spec.MaxReplicas)
			got := targets(t, out)
			assert.Len(t, got, len(in.Consumers), "one External metric per consumer")
			for consumer, want := range tc.targets {
				assert.Equal(t, want, got[consumer], consumer)
			}
			b := out.HPA.Spec.Behavior
			assert.Equal(t, int32(0), *b.ScaleUp.StabilizationWindowSeconds)
			assert.Equal(t, tc.max, *b.ScaleUp.Policies[0].Value, "the first step from zero may reach maxReplicas")
			assert.Equal(t, tc.down, *b.ScaleDown.StabilizationWindowSeconds)
			assert.Equal(t, tc.wantReasons, reasons(out))
		})
	}
}
