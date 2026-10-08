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

package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/mediactl/clustarr/internal/cli/agent"
)

// Both of these constants shipped disagreeing with the manifest, and neither
// disagreement could fail anything: DefaultIndexPath pointed at /index, which
// readOnlyRootFilesystem makes unwritable, and DefaultFacadeBindAddress bound
// Prowlarr's 9696 while the Service routes 8080. A deployed pod takes the env
// var and the containerPort, so the compiled-in values were only ever reached
// by someone running the binary directly -- which is exactly the person least
// equipped to diagnose a read-only filesystem or an unroutable port.
//
// These tests read what BOTH installers render rather than restating its
// values. A test that restates the value it guards cannot catch drift; that
// is how ValidKVKey shipped permissive in Phase C, pinned against a regex
// copied into the test file instead of against the server that rejects the
// key.

func TestDefaultIndexPathMatchesTheManifest(t *testing.T) {
	for name, r := range installerRenders(t) {
		_, c := renderedContainer(t, r, "agent-index")
		var got []string
		for _, e := range c.Env {
			if e.Name == "CLUSTARR_INDEX_PATH" {
				got = append(got, e.Value)
			}
		}
		require.Len(t, got, 1, "%s: the agent-index container sets CLUSTARR_INDEX_PATH once -- if it moved, this test must follow it", name)
		assert.Equal(t, got[0], agent.DefaultIndexPath,
			"%s: the compiled-in index path and the installer's CLUSTARR_INDEX_PATH disagree; "+
				"the pod runs readOnlyRootFilesystem, so a path outside the PVC mount cannot be written", name)
	}
}

func TestDefaultFacadeBindAddressMatchesTheServicePort(t *testing.T) {
	for name, r := range installerRenders(t) {
		d, c := renderedContainer(t, r, "agent-index")
		var port int32
		for _, p := range c.Ports {
			if p.Name == "http" {
				port = p.ContainerPort
			}
		}
		require.NotZero(t, port, "%s: no named http port on agent-index -- if it was renamed, this test must follow it", name)
		assert.Equal(t, fmt.Sprintf(":%d", port), agent.DefaultFacadeBindAddress,
			"%s: the facade would bind a port the container does not expose, so it would be unreachable", name)

		// The Service in front of the index pods routes to that port.
		var routed []corev1.ServicePort
		for _, svc := range r.Services {
			if len(svc.Spec.Selector) == 0 || !selects(svc.Spec.Selector, d.Spec.Template.Labels) {
				continue
			}
			for _, sp := range svc.Spec.Ports {
				if sp.TargetPort == intstr.FromString("http") || sp.TargetPort == intstr.FromInt32(port) {
					routed = append(routed, sp)
				}
			}
		}
		assert.NotEmpty(t, routed, "%s: no Service selecting the agent-index pods routes the facade's port", name)
	}
}

// selects reports whether selector matches labels.
func selects(selector, labels map[string]string) bool {
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}
