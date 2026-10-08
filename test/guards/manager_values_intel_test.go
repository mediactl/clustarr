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

package guards

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/internal/cli/ctrlflags"
	"github.com/mediactl/clustarr/test/installtest"
)

// TestChartIntelRenderGroupsReachTheController follows the chart's
// manager.intelRenderGroups value (X14) through what the chart renders: the
// Deployment that renders the transcode pools (the one carrying
// CLUSTARR_NATIVE_IMAGE) must carry the same GIDs in
// CLUSTARR_INTEL_RENDER_GROUPS, in the form the flag parses -- and no value
// must give none, since the render GID is per host install. That the
// variable reaches --intel-render-groups is cmd/manager's own test (W5.7).
func TestChartIntelRenderGroupsReachTheController(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want []int64
	}{
		"set":   {args: []string{"--set", "manager.intelRenderGroups={44,109}"}, want: []int64{44, 109}},
		"unset": {},
	} {
		t.Run(name, func(t *testing.T) {
			var got []int64
			var found bool
			for _, d := range installtest.DecodeRendered(t, installtest.RenderChart(t, tc.args...)).Deployments {
				env := map[string]string{}
				for _, e := range d.Spec.Template.Spec.Containers[0].Env {
					env[e.Name] = e.Value
				}
				if _, ok := env[cli.EnvNativeImage]; !ok {
					continue
				}
				found = true
				var err error
				got, err = ctrlflags.ParseGIDs(env[cli.EnvIntelRenderGroups])
				require.NoError(t, err)
			}
			require.True(t, found, "no Deployment renders the transcode pools")
			require.Equal(t, tc.want, got,
				"the chart's manager.intelRenderGroups did not reach the pools' renderer's --intel-render-groups")
		})
	}
}
