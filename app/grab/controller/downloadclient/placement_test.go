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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/pkg/placement"
)

// TestEnginePodCarriesTheWorkerPlacement is the control-plane placement
// design's §5.6 for the engines: a DownloadClient's own nodeSelector (usenet
// pinned to talos-b8) stays, and the workers profile joins it as a required
// node affinity.
func TestEnginePodCarriesTheWorkerPlacement(t *testing.T) {
	workers := placement.Profile{NodeAffinity: []corev1.NodeSelectorRequirement{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist},
		{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64"}},
	}}
	dc := usenetClient("nzb")
	dc.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "talos-b8-ae-ed-76-79-61"}

	dep := buildDeployment(dc, "nzb-engine", "img", "/data", "/scratch", "clustarr-data",
		EngineRuntime{WorkerPlacement: workers}, nil, fakeOwnerRef())

	spec := dep.Spec.Template.Spec
	assert.Equal(t, dc.Spec.NodeSelector, spec.NodeSelector)
	require.NotNil(t, spec.Affinity)
	terms := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	require.Len(t, terms[0].MatchExpressions, 2)
	assert.Equal(t, "node-role.kubernetes.io/control-plane", *terms[0].MatchExpressions[0].Key)
	assert.Equal(t, []string{"amd64"}, terms[0].MatchExpressions[1].Values)
}

func TestEnginePodWithoutAWorkerPlacementHasNoAffinity(t *testing.T) {
	dep := buildDeployment(usenetClient("nzb"), "nzb-engine", "img", "/data", "/scratch", "clustarr-data",
		EngineRuntime{}, nil, fakeOwnerRef())
	assert.Nil(t, dep.Spec.Template.Spec.Affinity)
}
