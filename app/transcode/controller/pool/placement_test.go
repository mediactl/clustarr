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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/placement"
)

var workersProfile = placement.Profile{NodeAffinity: []corev1.NodeSelectorRequirement{
	{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist},
	{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64"}},
}}

// TestEveryPoolClassCarriesTheWorkerPlacement is the control-plane placement
// design's §5.6: a cpu pool had no placement at all, so nothing kept an
// amd64-only native pod off an arm64 control-plane node; a GPU class keeps
// its own label expression in the same term.
func TestEveryPoolClassCarriesTheWorkerPlacement(t *testing.T) {
	c := Config{Image: "native:1", WorkerPlacement: workersProfile}
	for _, hw := range []transcodev1alpha1.Hardware{
		transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareNVIDIA,
	} {
		t.Run(string(hw), func(t *testing.T) {
			pod := Template(profile(), hw, c)
			require.NotNil(t, pod.Spec.Affinity)
			terms := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			require.Len(t, terms, 1)
			for _, want := range workersProfile.NodeAffinity {
				assert.Contains(t, terms[0].MatchExpressions, want)
			}
			if hw != transcodev1alpha1.HardwareCPU {
				assert.Contains(t, terms[0].MatchExpressions, corev1.NodeSelectorRequirement{
					Key: c.NodeLabel(hw), Operator: corev1.NodeSelectorOpIn, Values: []string{"true"},
				}, "the GPU label stays in the same term")
			}
		})
	}
}

func TestNoWorkerPlacementLeavesTheCPUPoolUnplaced(t *testing.T) {
	pod := Template(profile(), transcodev1alpha1.HardwareCPU, Config{Image: "native:1"})
	assert.Nil(t, pod.Spec.Affinity)
}
