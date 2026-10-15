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

package agentdomain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

// markers runs a pod per node, and only markers: every other domain keeps
// its slots target.
func TestOnlyMarkersScalesPerNode(t *testing.T) {
	var perNode []string
	for _, d := range agentdomain.Domains() {
		if d.PerNode {
			perNode = append(perNode, d.Name)
			assert.True(t, d.Autoscaled, "%s scales per node, so the manager's HPA owns it", d.Name)
		}
	}
	assert.Equal(t, []string{agentdomain.Markers}, perNode)
}

// A per-node domain's maxReplicas is its matching nodes, so each of its
// durables must feed every one of them at the most slots a pod may run:
// MaxAckPending below nodes x slots would leave the last pods starving
// (markers-analyze's 8 held three nodes at three slots to two pods).
func TestPerNodeDurablesFeedEveryNode(t *testing.T) {
	top := events.Default()
	for _, d := range agentdomain.Domains() {
		if !d.PerNode {
			continue
		}
		require.NotEmpty(t, d.Consumers, d.Name)
		for _, name := range d.Consumers {
			c, ok := top.Consumer(name)
			require.True(t, ok, name)
			assert.GreaterOrEqual(t, c.MaxAckPending, events.AutoscaleReplicaCeiling*events.PerNodeSlotsCeiling,
				"%s feeds %d nodes of up to %d slots", name, events.AutoscaleReplicaCeiling, events.PerNodeSlotsCeiling)
			_, throttled := agentdomain.Throttled()[name]
			assert.False(t, throttled, "%s: a throttled durable keeps one pod's slots", name)
		}
	}
}
