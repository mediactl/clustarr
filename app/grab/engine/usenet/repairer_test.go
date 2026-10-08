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

package usenet

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	usenetclient "github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/par2child"
)

// TestTheEngineRepairsInAChildOfItself (split spec 2026-10-06 §8.9 guard
// 5): the installed Repairer runs par2go in a child of the agent binary --
// the zero Exec, os.Executable() par2-repair with only the child's
// environment -- never in the engine's own process.
func TestTheEngineRepairsInAChildOfItself(t *testing.T) {
	lr, ok := newRepairer().(*usenetclient.LibraryRepairer)
	require.True(t, ok, "the engine's repairer is par2go's")
	assert.Equal(t, par2child.Exec{}, lr.Runner)
}

// The gauge is registered by every BuildClient, and an engine builds its
// client more than once in a process (a re-attach after a spec change):
// the second registration is no error and no second series.
func TestTheUnreapedChildrenGaugeRegistersOnce(t *testing.T) {
	r := prometheus.NewRegistry()
	require.NoError(t, registerUnreaped(r))
	require.NoError(t, registerUnreaped(r))
	// Gathered by hand: prometheus/testutil needs a module go.mod does not
	// declare (app/indexer/worker/rss's counterValue says the same).
	mfs, err := r.Gather()
	require.NoError(t, err)
	var series []float64
	for _, mf := range mfs {
		if mf.GetName() != "clustarr_par2_unreaped_children" {
			continue
		}
		assert.Equal(t, "GAUGE", mf.GetType().String())
		for _, m := range mf.GetMetric() {
			series = append(series, m.GetGauge().GetValue())
		}
	}
	assert.Equal(t, []float64{float64(par2child.Unreaped())}, series, "one series, reading par2child.Unreaped")
}
