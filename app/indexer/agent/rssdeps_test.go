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

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/indexer/clientcache"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The RSS poll production builds reserves its requests on the SAME query
// ring the search fan-out reserves on, through the bus it is handed -- a
// poll with no bus would neither count nor honour spec.limits.queryLimit --
// and reports through the health writer the search fan-out shares, so their
// read-modify-writes of an indexer's clustarr-indexer-health record are one
// in-process compare-and-swap (ADR-0019 §7.5). It writes no status, so it
// is handed no uncached reader any more (A6.3).
func TestTheRSSPollGetsTheBusAndTheSharedHealthWriter(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	hw := newHealthWriter(bus)

	deps := rssDeps(c, hw, bus, nil, clientcache.NewClientCache(c, nil))
	require.Equal(t, bus, deps.Bus, "the agent must hand the RSS poll the bus its query ring lives on")
	require.Same(t, hw, deps.Health, "the agent must hand the RSS poll the search fan-out's health writer")
	require.Equal(t, client.Reader(c), deps.Client)
}
