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

package ffmetrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ffruntime/ffmetrics"
)

func TestRegisterExportsTheAbandonedCallsGauge(t *testing.T) {
	r := prometheus.NewRegistry()
	require.NoError(t, ffmetrics.Register(r))
	require.NoError(t, ffmetrics.Register(r), "a second registration is not an error")
	// Gathered directly: prometheus/testutil needs a module go.mod does not
	// declare (app/catalog/metadata/tieredcache_test.go).
	fams, err := r.Gather()
	require.NoError(t, err)
	n := 0
	for _, f := range fams {
		if f.GetName() == "clustarr_ffgo_abandoned_calls" {
			n += len(f.GetMetric())
			assert.Zero(t, f.GetMetric()[0].GetGauge().GetValue(), "no call is abandoned in a fresh process")
		}
	}
	assert.Equal(t, 1, n)
}
