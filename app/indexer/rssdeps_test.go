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

package indexarr

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The RSS poll production builds reserves its requests on the SAME query
// ring the search fan-out reserves on, through the bus it is handed -- a
// poll with no bus would neither count nor honour spec.limits.queryLimit --
// and writes its status through the uncached reader, which its
// compare-and-swap needs: a cached read lags the write it raced.
func TestTheRSSPollGetsTheBusAndAnUncachedReader(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	reader := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()

	deps := rssDeps(c, reader, bus, nil, indexer.NewClientCache(c, nil))
	require.Equal(t, bus, deps.Bus, "run.go must hand the RSS poll the bus its query ring lives on")
	require.Equal(t, client.Reader(reader), deps.Reader, "run.go must hand the RSS poll the uncached reader")
}
