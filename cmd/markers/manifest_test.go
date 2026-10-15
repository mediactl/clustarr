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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/test/installtest"
)

// Both installers' markers args parse through this binary's own flags, and
// --cores-per-slot carries the chart's markers.coresPerSlot: envtest runs no
// pods, so nothing else would catch a flag the binary no longer takes.
func TestInstallersMarkersArgsParse(t *testing.T) {
	for name, tc := range map[string]struct {
		out  []byte
		want int
	}{
		"helm":                       {installtest.RenderChart(t), events.DefaultCoresPerSlot},
		"helm, coresPerSlot 1":       {installtest.RenderChart(t, "--set", "markers.coresPerSlot=1"), 1},
		"kustomize":                  {installtest.BuildKustomize(t, "config/default"), events.DefaultCoresPerSlot},
		"helm, autoscaling disabled": {installtest.RenderChart(t, "--set", "autoscaling.enabled=false"), events.DefaultCoresPerSlot},
	} {
		t.Run(name, func(t *testing.T) {
			d, ok := installtest.Workloads(installtest.DecodeRendered(t, tc.out))["markers"]
			require.True(t, ok, "no markers Deployment")
			c := d.Spec.Template.Spec.Containers[0]
			require.Equal(t, []string{binpath.Markers}, c.Command)
			f := newFlags()
			require.NoError(t, f.fs.Parse(c.Args))
			assert.Equal(t, tc.want, *f.coresPerSlot)
			assert.Zero(t, *f.threads, "decode threads follow the slots")
		})
	}
}
