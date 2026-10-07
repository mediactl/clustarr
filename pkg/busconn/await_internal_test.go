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

package busconn

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func fastPoll(t *testing.T) {
	t.Helper()
	old := awaitPoll
	awaitPoll = 10 * time.Millisecond
	t.Cleanup(func() { awaitPoll = old })
}

// An agent that starts before the manager waits, then proceeds once the
// manager has ensured the topology (spec §3.5.2 step 7).
func TestAwaitTopologyReturnsOnceTheManagerHasEnsuredIt(t *testing.T) {
	fastPoll(t)
	ctx := t.Context()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	topo := contracttest.Topology()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = bus.Ensure(ctx, topo)
	}()
	require.NoError(t, AwaitTopology(ctx, bus, topo, 5*time.Second))
}

// On timeout the error names exactly what is still missing, so a crash-looping
// agent's log says which object the manager has not made.
func TestAwaitTopologyNamesWhatIsStillMissingWhenItGivesUp(t *testing.T) {
	fastPoll(t)
	ctx := t.Context()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	topo := contracttest.Topology()
	require.NotEmpty(t, topo.Buckets)
	partial := topo
	partial.Buckets = nil
	require.NoError(t, bus.Ensure(ctx, partial))

	err := AwaitTopology(ctx, bus, topo, 100*time.Millisecond)
	require.ErrorContains(t, err, "waiting for the manager to create ")
	for _, b := range topo.Buckets {
		require.ErrorContains(t, err, "kv "+b.Name)
	}
	for _, s := range topo.Streams {
		require.NotContains(t, err.Error(), "stream "+s.Name, "the streams exist")
	}
}

type adminless struct{ events.Bus }

func TestAwaitTopologyNeedsABusThatCanReportWhatIsMissing(t *testing.T) {
	err := AwaitTopology(t.Context(), adminless{}, contracttest.Topology(), time.Second)
	require.ErrorContains(t, err, "cannot report")
}
