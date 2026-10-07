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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

func TestTopologyObjectsListsEveryObjectInKindOrder(t *testing.T) {
	topo := events.Topology{
		Streams:      []events.StreamSpec{{Name: "S1"}, {Name: "S2"}},
		Consumers:    []events.ConsumerSpec{{Name: "c1", Stream: "S2"}},
		Buckets:      []events.BucketSpec{{Name: "b1"}},
		ObjectStores: []events.ObjectStoreSpec{{Name: "o1"}},
	}
	var got []string
	for _, o := range topo.Objects() {
		got = append(got, o.String())
	}
	assert.Equal(t, []string{"stream S1", "stream S2", "consumer S2/c1", "kv b1", "object store o1"}, got)
}

// Missing reports by these names, so two objects of the default topology
// sharing one would hide one of them from AwaitTopology's error.
func TestDefaultTopologyObjectNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range events.Default().Objects() {
		require.False(t, seen[o.String()], "%s named twice", o)
		seen[o.String()] = true
	}
}
