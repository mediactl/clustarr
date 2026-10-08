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

package remediation_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheLoopLinksNoWorkerCode: the loop runs in the manager, which never
// probes, decodes or downloads (split R3, R6): the loop, its planners,
// stages, actuators and registration link no prober, no FFmpeg, no par2, no
// torrent engine and no worker or agent package. Test support
// (remediationtest, itemtest) is left out: no binary links it.
func TestTheLoopLinksNoWorkerCode(t *testing.T) {
	out, err := exec.Command("go", "list", "./...").Output()
	require.NoError(t, err, "go list")
	var pkgs []string
	for _, p := range strings.Fields(string(out)) {
		if strings.HasSuffix(p, "/remediationtest") || strings.HasSuffix(p, "/itemtest") {
			continue
		}
		pkgs = append(pkgs, p)
	}
	require.Contains(t, pkgs, "github.com/mediactl/clustarr/app/remediation/manager")
	out, err = exec.Command("go", append([]string{"list", "-deps"}, pkgs...)...).Output()
	require.NoError(t, err, "go list -deps")
	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/mediactl/clustarr/app/remediation", "go list -deps listed nothing")
	banned := []string{
		"github.com/mediactl/clustarr/pkg/mediainfo/native",
		"github.com/obinnaokechukwu/ffgo",
		"github.com/ebitengine/purego",
		"github.com/mediactl/par2go",
		"github.com/anacrolix/",
		"github.com/shota3506/onnxruntime-purego",
	}
	for _, dep := range deps {
		for _, b := range banned {
			if strings.HasPrefix(dep, b) {
				t.Errorf("app/remediation depends on %s", dep)
			}
		}
		// Any domain's worker or agent tree: app/<domain>/worker/..., app/<domain>/agent/...
		if parts := strings.Split(strings.TrimPrefix(dep, "github.com/mediactl/clustarr/"), "/"); strings.HasPrefix(dep, "github.com/mediactl/clustarr/app/") &&
			len(parts) >= 3 && (parts[2] == "worker" || parts[2] == "agent") {
			t.Errorf("app/remediation depends on %s", dep)
		}
	}
}
