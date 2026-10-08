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

package probe_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestTheProbeAdaptersSources: the probe planner wakes the loop from
// clustarr-probes' answers (S16) and nothing else -- a transcode swap's probe
// is asked by its Swapping block, which the transcode record's waker (S17)
// wakes, and a graft's by the graft block's (S18); S23's TranscodeJob and
// AudioGraft rows went with those kinds.
func TestTheProbeAdaptersSources(t *testing.T) {
	h := newProbeHarness(t)
	e := remediationtest.Start(t)
	mgr, err := ctrl.NewManager(e.Config, ctrl.Options{
		Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	srcs, err := h.Adapter.Sources(mgr)
	require.NoError(t, err)
	var names []string
	for _, s := range srcs {
		names = append(names, s.Name)
		assert.NotNil(t, s.Raw, "%s is a records source", s.Name)
	}
	assert.Equal(t, []string{"S16/clustarr-probes"}, names)
}
