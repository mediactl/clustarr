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

package controller

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// MatchingNodes counts the nodes a pod with spec could be scheduled on, as
// far as the node alone decides: Ready and not cordoned; matched by the
// pod's nodeSelector and required node affinity; free of any NoSchedule or
// NoExecute taint the pod does not tolerate; and allocating some of every
// extended resource the pod requests. It is the HPA's maxReplicas before
// the consumer clamp, and generalises squasharr's gpuNodes. Capacity
// (cpu, memory, ephemeral-storage, hugepages) is not counted: a busy node
// still counts, the scheduler decides.
func MatchingNodes(nodes []corev1.Node, spec *corev1.PodSpec) (int, error) {
	required := nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: *spec})
	extended := extendedResources(spec)
	n := 0
	for i := range nodes {
		node := &nodes[i]
		if node.Spec.Unschedulable || !nodeReady(node) {
			continue
		}
		ok, err := required.Match(node)
		if err != nil {
			return 0, fmt.Errorf("autoscale: the pod template's required node affinity: %w", err)
		}
		if !ok {
			continue
		}
		if _, untolerated := corev1helpers.FindMatchingUntoleratedTaint(
			logr.Discard(), node.Spec.Taints, spec.Tolerations, blocksScheduling, false); untolerated {
			continue
		}
		if !allocates(node, extended) {
			continue
		}
		n++
	}
	return n, nil
}

// blocksScheduling keeps the taints a new pod must tolerate to land.
func blocksScheduling(t *corev1.Taint) bool {
	return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
}

// extendedResources is every resource a container or init container
// requests or limits other than cpu, memory, ephemeral-storage and
// hugepages.
func extendedResources(spec *corev1.PodSpec) []corev1.ResourceName {
	seen := map[corev1.ResourceName]bool{}
	var out []corev1.ResourceName
	add := func(list corev1.ResourceList) {
		for name := range list {
			switch {
			case name == corev1.ResourceCPU, name == corev1.ResourceMemory, name == corev1.ResourceEphemeralStorage,
				strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix), seen[name]:
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			add(cs[i].Resources.Requests)
			add(cs[i].Resources.Limits)
		}
	}
	return out
}

func allocates(node *corev1.Node, resources []corev1.ResourceName) bool {
	for _, r := range resources {
		q, ok := node.Status.Allocatable[r]
		if !ok || q.Sign() <= 0 {
			return false
		}
	}
	return true
}

// nodeReady reports whether n's Ready condition is True.
func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
