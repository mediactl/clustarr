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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

// TestChartIntelRenderGroupsReachTheController follows the chart's
// squasharr.intelRenderGroups value (X14) through what the chart renders:
// the squasharr Deployment's env, run through the real command tree, must
// give squasharr the same GIDs -- and no value must give none, since the
// render GID is per host install.
func TestChartIntelRenderGroupsReachTheController(t *testing.T) {
	helm := findTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		args []string
		want []int64
	}{
		"set":   {args: []string{"--set", "squasharr.intelRenderGroups={44,109}"}, want: []int64{44, 109}},
		"unset": {},
	} {
		t.Run(name, func(t *testing.T) {
			out := run(t, root, helm, append([]string{"template", "clustarr", "charts/clustarr"}, tc.args...)...)
			r := decodeRendered(t, out)
			var dep *appsv1.Deployment
			for i := range r.deployments {
				if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "squasharr" {
					dep = &r.deployments[i]
				}
			}
			require.NotNil(t, dep, "no squasharr Deployment rendered")
			ctr := dep.Spec.Template.Spec.Containers[0]
			t.Setenv(intelRenderGroupsEnv, "")
			for _, e := range ctr.Env {
				if e.ValueFrom == nil {
					t.Setenv(e.Name, e.Value)
				}
			}
			t.Setenv(namespaceEnv, "clustarr-system")
			got := stub(t, &runSquasharr)
			_, err := execute(t, ctr.Args...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.IntelRenderGroups,
				"the chart's squasharr.intelRenderGroups did not reach squasharr's --intel-render-groups")
		})
	}
}
