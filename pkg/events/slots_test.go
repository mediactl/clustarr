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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestDefaultConsumersCarryTheirSlots pins spec §9.1.1's per-pod slots: each
// consumer's per-pod handler count, except the artwork renderer (the process
// draws two at a time, so 32 slots would make an HPA under-count its load)
// and segment analysis (the shipped --concurrency default); the probe lanes'
// and the recycle sweep's from §6.5.1 and §3.5.3, and the manager's intake
// and task-events durables' from ADR-0019 (A1.6's table).
func TestDefaultConsumersCarryTheirSlots(t *testing.T) {
	want := map[string]int{
		events.ConsumerCatalogRSSMatcher:    256,
		events.ConsumerCatalogSearchHigh:    8,
		events.ConsumerCatalogSearchNorm:    8,
		events.ConsumerCatalogMetadata:      32,
		events.ConsumerCatalogArtworkFetch:  32,
		events.ConsumerCatalogArtworkRender: 4,
		events.ConsumerCatalogMarkers:       32,
		events.ConsumerCatalogSegmentsPlan:  8,
		events.ConsumerMarkersAnalyze:       1,
		events.ConsumerCatalogHistory:       512,
		events.ConsumerImportScan:           4,
		events.ConsumerImportList:           2,
		events.ConsumerImportFile:           4,
		events.ConsumerImportRecycle:        1,
		events.ConsumerIndexRSS:             4,
		events.ConsumerCaptionFetchHigh:     16,
		events.ConsumerCaptionFetchNormal:   16,
		events.ConsumerDLQProjector:         64,
		events.ConsumerImportProbeHigh:      4,
		events.ConsumerImportProbeLow:       4,
		events.ConsumerIntakeCandidate:      64,
		events.ConsumerIntakeScan:           8,
		events.ConsumerTaskEvents:           16,
	}
	got := map[string]int{}
	for _, c := range events.Default().Consumers {
		if c.Stream == events.StreamAdvisories {
			continue // the dead-letter watchers have their own test
		}
		got[c.Name] = c.Slots
	}
	assert.Equal(t, want, got)
}

// A pool worker runs one task at a time, but its durable's cap across every
// puller stays 64: Pull writes Subscription.MaxAckPending, so one slot must
// not shrink it.
func TestAPoolDurableKeepsItsCapThroughItsSubscription(t *testing.T) {
	c := events.TranscodeTaskConsumer("6f1c-uid", "cpu")
	assert.Equal(t, 1, c.Slots)
	assert.Equal(t, 64, c.MaxAckPending)
	sub := c.Subscription()
	assert.Equal(t, 1, sub.MaxInFlight)
	assert.Equal(t, 64, sub.MaxAckPending)
	assert.Equal(t, 64, events.SubscriptionSpec(sub).MaxAckPending)
	assert.Equal(t, 1, events.SubscriptionSpec(sub).Slots)
}

func TestSubscriptionSpecInvertsSubscription(t *testing.T) {
	top := events.Default()
	consumers := append([]events.ConsumerSpec{
		events.TranscodeTaskConsumer("6f1c-uid", "nvidia"),
		events.EngineConsumer("qbit", 1),
	}, top.Consumers...)
	for _, c := range consumers {
		want := c
		// A Subscription carries neither: Description is the broker's
		// label, Dispatched the manager's routing of the durable's
		// advisories (TaskEventSubjects), and ConsumerConfig reads only
		// the former.
		want.Description = ""
		want.Dispatched = false
		assert.Equal(t, want, events.SubscriptionSpec(c.Subscription()), c.Name)
	}
	// The defaults both buses apply: one slot, and a cap of the slots.
	got := events.SubscriptionSpec(events.Subscription{Stream: "S", Durable: "d", Filters: []string{"a.>"}})
	assert.Equal(t, 1, got.Slots)
	assert.Equal(t, 1, got.MaxAckPending)
	got = events.SubscriptionSpec(events.Subscription{Stream: "S", Durable: "d", Filters: []string{"a.>"}, MaxInFlight: 3})
	assert.Equal(t, 3, got.Slots)
	assert.Equal(t, 3, got.MaxAckPending)
}

func TestAckDeadline(t *testing.T) {
	s := time.Second
	for _, tc := range []struct {
		name    string
		sub     events.Subscription
		attempt uint64
		want    time.Duration
	}{
		{"no backoff and no AckWait is JetStream's default", events.Subscription{}, 1, events.DefaultAckWait},
		{"no backoff: AckWait for every attempt", events.Subscription{AckWait: 45 * s}, 3, 45 * s},
		{"first delivery: Backoff[0], not AckWait", events.Subscription{AckWait: 120 * s, Backoff: []time.Duration{30 * s, 2 * time.Minute}}, 1, 30 * s},
		{"attempt 0 reads as the first", events.Subscription{Backoff: []time.Duration{10 * s}}, 0, 10 * s},
		{"delivery n: Backoff[n-1]", events.Subscription{Backoff: []time.Duration{s, 5 * s, 30 * s}}, 2, 5 * s},
		{"the last entry is delivery len(Backoff)'s", events.Subscription{Backoff: []time.Duration{s, 5 * s, 30 * s}}, 3, 30 * s},
		{"past the end: the last entry", events.Subscription{Backoff: []time.Duration{s, 5 * s}}, 9, 5 * s},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, events.AckDeadline(tc.sub, tc.attempt))
		})
	}
	assert.Equal(t, 30*time.Second, events.DefaultAckWait, "nats-server's JsAckWaitDefault")
}

func TestParseSlotOverrides(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    map[string]int
		wantErr string
	}{
		{"", map[string]int{}, ""},
		{"  ", map[string]int{}, ""},
		{"markers-analyze=3", map[string]int{"markers-analyze": 3}, ""},
		{" markers-analyze = 3 , catalog-metadata=8 ", map[string]int{"markers-analyze": 3, "catalog-metadata": 8}, ""},
		{"markers-analyze=0", nil, "at least 1"},
		{"markers-analyze=-2", nil, "at least 1"},
		{"markers-analyze=three", nil, "at least 1"},
		{"markers-analyze", nil, "<durable>=<slots>"},
		{"=3", nil, "<durable>=<slots>"},
		{"a=1,a=2", nil, "named twice"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := events.ParseSlotOverrides(tc.in)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.ErrorContains(t, err, events.SlotsEnv)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	assert.Equal(t, "CLUSTARR_CONSUMER_SLOTS", events.SlotsEnv)
}

func TestSlotsFor(t *testing.T) {
	c := events.ConsumerSpec{Name: "markers-analyze", Slots: 1}
	assert.Equal(t, 3, events.SlotsFor(c, map[string]int{"markers-analyze": 3}))
	assert.Equal(t, 1, events.SlotsFor(c, map[string]int{"catalog-metadata": 8}))
	assert.Equal(t, 1, events.SlotsFor(c, nil))
}

// A per-node domain's pod sizes its own slots from the CPUs it can use: one
// per coresPerSlot of them, so markers on talos' 4-CPU workers runs two
// analyses and on its 16-CPU worker eight, where a fixed three left the big
// node idle and oversubscribed the small ones (2026-10-15).
func TestSlotsForCPUsIsOnePerCoresPerSlot(t *testing.T) {
	for _, tc := range []struct{ cpus, coresPerSlot, want int }{
		{4, events.DefaultCoresPerSlot, 2},
		{5, events.DefaultCoresPerSlot, 2},
		{6, events.DefaultCoresPerSlot, 3},
		{12, events.DefaultCoresPerSlot, 6},
		{16, events.DefaultCoresPerSlot, 8},
		// An I/O-bound cluster (one 1 GbE NFS link a node) runs a slot a core.
		{4, 1, 4},
		{6, 1, 6},
		{12, 3, 4},
	} {
		assert.Equal(t, tc.want, events.SlotsForCPUs(tc.cpus, tc.coresPerSlot), "%d CPUs at %d a slot", tc.cpus, tc.coresPerSlot)
	}
}

// The automatic slots are at least one, whatever the pod's CPUs, and at most
// PerNodeSlotsCeiling, the most a per-node durable's MaxAckPending feeds on
// every node (TestPerNodeDurablesFeedEveryNode); a divisor below one counts
// as one.
func TestSlotsForCPUsClampsToOneAndThePerNodeCeiling(t *testing.T) {
	for _, tc := range []struct{ cpus, coresPerSlot, want int }{
		{0, events.DefaultCoresPerSlot, 1},
		{1, events.DefaultCoresPerSlot, 1},
		{3, events.DefaultCoresPerSlot, 1},
		{2, 4, 1},
		{17, events.DefaultCoresPerSlot, events.PerNodeSlotsCeiling},
		{64, events.DefaultCoresPerSlot, events.PerNodeSlotsCeiling},
		{16, 1, events.PerNodeSlotsCeiling},
		{4, 0, 4},
		{4, -2, 4},
	} {
		assert.Equal(t, tc.want, events.SlotsForCPUs(tc.cpus, tc.coresPerSlot), "%d CPUs at %d a slot", tc.cpus, tc.coresPerSlot)
	}
}
