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

// TestIntelRenderGroupsReachTheManager: manager.intelRenderGroups renders
// the manager's CLUSTARR_INTEL_RENDER_GROUPS in the form --intel-render-groups
// parses (X14), which the manager stamps onto every Intel transcode pod as
// supplementalGroups; unset renders nothing, since the render GID is per
// host install. cmd/manager parses it (W5.7).
func TestIntelRenderGroupsReachTheManager(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want []int64
	}{
		"set":   {args: []string{"--set", "manager.intelRenderGroups={44,109}"}, want: []int64{44, 109}},
		"unset": {},
	} {
		t.Run(name, func(t *testing.T) {
			c := installtest.Container(t, installtest.DecodeRendered(t, installtest.RenderChart(t, tc.args...)), "manager")
			v, _ := containerEnv(c, cli.EnvIntelRenderGroups)
			got, err := ctrlflags.ParseGIDs(v)
			require.NoError(t, err)
			require.Equal(t, tc.want, got, "the chart's manager.intelRenderGroups did not reach the manager's --intel-render-groups")
		})
	}
}
