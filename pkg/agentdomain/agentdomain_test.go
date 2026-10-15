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

// A per-node domain's pod sizes its slots from its own CPUs unless
// CLUSTARR_CONSUMER_SLOTS (chart markers.consumerSlots) names the consumer:
// an explicit override always wins, and a domain that does not scale per
// node keeps its topology's slots whatever its node.
func TestAnOverrideWinsOverAPerNodeDomainsAutomaticSlots(t *testing.T) {
	markers, ok := agentdomain.Lookup(agentdomain.Markers)
	require.True(t, ok)
	analyze, ok := events.Default().Consumer(events.ConsumerMarkersAnalyze)
	require.True(t, ok)
	imp, ok := agentdomain.Lookup(agentdomain.Import)
	require.True(t, ok)
	scan, ok := events.Default().Consumer(events.ConsumerImportScan)
	require.True(t, ok)
	override := map[string]int{events.ConsumerMarkersAnalyze: 3}
	other := map[string]int{events.ConsumerImportScan: 8}

	assert.True(t, markers.AutoSlots(events.ConsumerMarkersAnalyze, nil), "no override: automatic")
	assert.True(t, markers.AutoSlots(events.ConsumerMarkersAnalyze, other), "an override of another consumer leaves it automatic")
	assert.False(t, markers.AutoSlots(events.ConsumerMarkersAnalyze, override), "the override wins")
	assert.False(t, imp.AutoSlots(events.ConsumerImportScan, nil), "import does not scale per node")

	for _, tc := range []struct {
		name      string
		d         agentdomain.Domain
		c         events.ConsumerSpec
		overrides map[string]int
		cpus      int
		want      int
	}{
		{"markers on a 4-CPU node", markers, analyze, nil, 4, 2},
		{"markers on a 16-CPU node", markers, analyze, nil, 16, 8},
		{"markers on a 1-CPU node", markers, analyze, nil, 1, 1},
		{"markers past the ceiling", markers, analyze, nil, 64, events.PerNodeSlotsCeiling},
		{"markers' override on a 16-CPU node", markers, analyze, override, 16, 3},
		{"markers' override on a 4-CPU node", markers, analyze, override, 4, 3},
		{"import keeps its topology's slots", imp, scan, nil, 16, scan.Slots},
		{"import's own override", imp, scan, other, 4, 8},
	} {
		assert.Equal(t, tc.want, tc.d.PodSlots(tc.c, tc.overrides, tc.cpus, events.DefaultCoresPerSlot), tc.name)
	}
	assert.Equal(t, 4, markers.PodSlots(analyze, nil, 4, 1), "a slot a core where /data, not the CPUs, binds")
}
