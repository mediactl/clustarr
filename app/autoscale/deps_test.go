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

package autoscale_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// R5: the External Metrics API is hand-rolled to keep the manager small,
// so app/autoscale links no API-server library (§4.5.1's Servers row,
// which test/guards' manager deps guard repeats for the whole binary). The
// component-helpers anchor keeps the check from passing on an empty list.
func TestAutoscaleLinksNoAPIServerLibraries(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	require.True(t, slices.Contains(deps, "k8s.io/component-helpers/scheduling/corev1/nodeaffinity"),
		"anchor missing: this guard is not looking at app/autoscale's closure")
	for _, dep := range deps {
		for _, banned := range []string{"k8s.io/apiserver", "k8s.io/kube-aggregator", "k8s.io/metrics", "sigs.k8s.io/custom-metrics-apiserver"} {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("app/autoscale links %s; the External Metrics API is hand-rolled (R5)", dep)
			}
		}
	}
}
