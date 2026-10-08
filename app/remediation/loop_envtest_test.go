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
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

func naming(path string) remediationtest.PlanFunc {
	return func(_ *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.Naming = &catalogv1alpha1.NamingStatus{ExpectedPath: path}
		return remediation.Result{}, nil
	}
}

// passes reads clustarr_remediation_passes_total for file keys through
// client_model, not prometheus/testutil, whose godebug dependency go.mod
// does not declare. No test in this package runs in parallel, so a delta
// around a pass is that pass's.
func passes(outcome string) float64 {
	var m dto.Metric
	if err := metrics.RemediationPassesTotal.WithLabelValues(string(remediation.KindMediaFile), outcome).Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

// A pass that changes nothing sends no status request at all (§3.7): about
// 95% of the old controller's status applies were no-ops.
func TestAnUnchangedPassAppliesNothing(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "unchanged", "heat-abc", "/data/movies/Heat (1995)/heat.mkv")
	r := e.Reconciler(t, remediation.Bind[int](remediationtest.NamingFake(naming("/data/movies/Heat (1995)/Heat (1995).mkv"))))
	e.Pass(t, r, nn)
	require.NotNil(t, e.Get(t, nn).Status.Naming, "setup: the first pass applied")
	before := e.StatusPatches()
	unchanged := passes("unchanged")
	for range 3 {
		e.Pass(t, r, nn)
	}
	assert.Equal(t, before, e.StatusPatches(), "an unchanged pass sent a status request")
	assert.Equal(t, unchanged+3, passes("unchanged"))
}
