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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/app/autoscale/controller"
)

type nodeOpt func(*corev1.Node)

func node(name string, opts ...nodeOpt) corev1.Node {
	n := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{},
		},
	}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func labelled(k, v string) nodeOpt { return func(n *corev1.Node) { n.Labels[k] = v } }
func notReady(n *corev1.Node)      { n.Status.Conditions[0].Status = corev1.ConditionFalse }
func noReadyCondition(n *corev1.Node) {
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}}
}
func cordoned(n *corev1.Node) { n.Spec.Unschedulable = true }
func tainted(key string, effect corev1.TaintEffect) nodeOpt {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Value: "x", Effect: effect})
	}
}
func allocatable(r corev1.ResourceName, q string) nodeOpt {
	return func(n *corev1.Node) { n.Status.Allocatable[r] = resource.MustParse(q) }
}

type specOpt func(*corev1.PodSpec)

func spec(opts ...specOpt) *corev1.PodSpec {
	s := &corev1.PodSpec{Containers: []corev1.Container{{Name: "agent"}}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func nodeSelector(k, v string) specOpt {
	return func(s *corev1.PodSpec) { s.NodeSelector = map[string]string{k: v} }
}

func affinity(key string, op corev1.NodeSelectorOperator, values ...string) specOpt {
	return func(s *corev1.PodSpec) {
		s.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: key, Operator: op, Values: values}},
			}}},
		}}
	}
}

func tolerates(key string, effect corev1.TaintEffect) specOpt {
	return func(s *corev1.PodSpec) {
		s.Tolerations = append(s.Tolerations, corev1.Toleration{Key: key, Operator: corev1.TolerationOpExists, Effect: effect})
	}
}

func requests(r corev1.ResourceName, q string) specOpt {
	return func(s *corev1.PodSpec) {
		s.Containers[0].Resources.Requests = corev1.ResourceList{r: resource.MustParse(q)}
	}
}

func initRequests(r corev1.ResourceName, q string) specOpt {
	return func(s *corev1.PodSpec) {
		s.InitContainers = []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{r: resource.MustParse(q)},
		}}}
	}
}

func TestMatchingNodes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes []corev1.Node
		spec  *corev1.PodSpec
		want  int
	}{
		{"a Ready, schedulable node with no constraints", []corev1.Node{node("a")}, spec(), 1},
		{"NotReady", []corev1.Node{node("a", notReady)}, spec(), 0},
		{"no Ready condition at all", []corev1.Node{node("a", noReadyCondition)}, spec(), 0},
		{"cordoned", []corev1.Node{node("a", cordoned)}, spec(), 0},
		{"nodeSelector miss", []corev1.Node{node("a"), node("b", labelled("role", "agent"))}, spec(nodeSelector("role", "agent")), 1},
		{"affinity In", []corev1.Node{node("a", labelled("zone", "a")), node("c", labelled("zone", "c"))},
			spec(affinity("zone", corev1.NodeSelectorOpIn, "a", "b")), 1},
		{"affinity NotIn matches an absent label", []corev1.Node{node("a", labelled("zone", "a")), node("c", labelled("zone", "c")), node("none")},
			spec(affinity("zone", corev1.NodeSelectorOpNotIn, "c")), 2},
		{"affinity Exists", []corev1.Node{node("gpu", labelled("gpu", "x")), node("plain")},
			spec(affinity("gpu", corev1.NodeSelectorOpExists)), 1},
		{"affinity DoesNotExist", []corev1.Node{node("gpu", labelled("gpu", "x")), node("plain"), node("other", labelled("zone", "a"))},
			spec(affinity("gpu", corev1.NodeSelectorOpDoesNotExist)), 2},
		{"an untolerated NoSchedule taint", []corev1.Node{node("a", tainted("dedicated", corev1.TaintEffectNoSchedule))}, spec(), 0},
		{"a tolerated NoSchedule taint", []corev1.Node{node("a", tainted("dedicated", corev1.TaintEffectNoSchedule))},
			spec(tolerates("dedicated", corev1.TaintEffectNoSchedule)), 1},
		{"an untolerated NoExecute taint", []corev1.Node{node("a", tainted("evict", corev1.TaintEffectNoExecute))}, spec(), 0},
		{"a PreferNoSchedule taint is ignored", []corev1.Node{node("a", tainted("soft", corev1.TaintEffectPreferNoSchedule))}, spec(), 1},
		{"an extended resource with zero allocatable",
			[]corev1.Node{node("none", allocatable("nvidia.com/gpu", "0")), node("one", allocatable("nvidia.com/gpu", "1"))},
			spec(requests("nvidia.com/gpu", "1")), 1},
		{"an extended resource absent from allocatable", []corev1.Node{node("a")}, spec(requests("nvidia.com/gpu", "1")), 0},
		{"cpu is never counted", []corev1.Node{node("a")}, spec(requests(corev1.ResourceCPU, "2")), 1},
		{"memory is never counted", []corev1.Node{node("a")}, spec(requests(corev1.ResourceMemory, "1Gi")), 1},
		{"ephemeral-storage is never counted", []corev1.Node{node("a")}, spec(requests(corev1.ResourceEphemeralStorage, "1Gi")), 1},
		{"hugepages are never counted", []corev1.Node{node("a")}, spec(requests("hugepages-2Mi", "2Mi")), 1},
		{"an init container's limit counts", []corev1.Node{node("a")}, spec(initRequests("gpu.intel.com/i915", "1")), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controller.MatchingNodes(tc.nodes, tc.spec)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A required affinity the scheduler could not parse matches no node, and
// the caller learns why.
func TestMatchingNodesReportsAnUnparseableAffinity(t *testing.T) {
	_, err := controller.MatchingNodes([]corev1.Node{node("a", labelled("cores", "8"))},
		spec(affinity("cores", corev1.NodeSelectorOpGt, "eight")))
	require.Error(t, err)
}
