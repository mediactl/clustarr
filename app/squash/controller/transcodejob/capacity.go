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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
)

// gpuNodes reports which GPU classes have a Ready, schedulable node that
// carries the class's GPU label as "true" and allocatable GPUs: the labels
// the GPU operators set, confirmed by the device plugins that make the GPU
// requestable (spec §18.5).
//
// The label is the one the class's pools are held to (pool.Config.NodeLabel,
// from --gpu-node-label-nvidia and --gpu-node-label-intel), and the resource
// the one their pods request (pool.GPUResource): one source for each, so the
// node that makes admission choose a class is a node that class's pods can
// land on.
//
// Nodes are read through Client, the manager's cache: every admission pass
// asks, and a Node informer answers without a request.
func (r *Reconciler) gpuNodes(ctx context.Context) (map[transcodev1alpha1.Hardware]bool, error) {
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("transcodejob: list Nodes: %w", err)
	}
	out := map[transcodev1alpha1.Hardware]bool{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Spec.Unschedulable || !nodeReady(n) {
			continue
		}
		for class, res := range pool.GPUResource {
			if n.Labels[r.Pool.NodeLabel(class)] != "true" {
				continue
			}
			if q, ok := n.Status.Allocatable[res]; ok && q.Sign() > 0 {
				out[class] = true
			}
		}
	}
	return out, nil
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
