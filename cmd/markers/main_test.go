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

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// markersAnalyze is markers' domain and its one durable.
func markersAnalyze(t *testing.T) (agentdomain.Domain, events.ConsumerSpec) {
	t.Helper()
	dom, ok := agentdomain.Lookup(agentdomain.Markers)
	require.True(t, ok)
	spec, ok := events.Default().Consumer(events.ConsumerMarkersAnalyze)
	require.True(t, ok)
	return dom, spec
}

func TestMissingNATSURLExitsMisconfigured(t *testing.T) {
	env := map[string]string{"POD_NAME": "markers-worker-0"}
	assert.Equal(t, worker.ExitMisconfigured, run(nil, func(k string) string { return env[k] }))
}

func TestABadFlagExitsMisconfigured(t *testing.T) {
	assert.Equal(t, worker.ExitMisconfigured, run([]string{"--no-such-flag"}, func(string) string { return "" }))
}

// A SIGTERMed markers pod hands its seasons back at once (RequeueOnStop)
// and waits only the requeue drain for them, not the 30-minute AckWait it
// once waited out on every rollout (2026-10-15); its whole bus shutdown fits
// a minute's grace.
func TestMarkersDrainsForItsStopBudgetNotItsAckWait(t *testing.T) {
	dom, spec := markersAnalyze(t)
	sub := subscription(dom, spec, map[string]int{events.ConsumerMarkersAnalyze: 3}, 16, events.DefaultCoresPerSlot)
	assert.Equal(t, 3, sub.MaxInFlight, "the slot override still applies")
	assert.True(t, sub.RequeueOnStop)
	assert.Equal(t, events.RequeueDrain, sub.Drain)
	assert.Less(t, sub.Drain, spec.AckWait)
	assert.LessOrEqual(t, shutdownBudget(sub)+tracingShutdown, time.Minute,
		"the bus shutdown and the tracer's flush fit the Deployment's 60 s grace")
}

// The drain waits for handlers, but never for the FFmpeg calls a stop left
// abandoned: an NFS read blocked in the kernel may never return, and the
// process exits regardless once the drain's budget is spent.
func TestStopWithinGivesUpOnAStopThatNeverReturns(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	begun := time.Now()
	assert.False(t, stopWithin(func() { <-blocked }, 50*time.Millisecond))
	assert.Less(t, time.Since(begun), 2*time.Second)
	assert.True(t, stopWithin(func() {}, time.Second))
}

// Without an override a markers pod runs one analysis per --cores-per-slot
// of the CPUs it can use, from 1 to PerNodeSlotsCeiling: talos' 4-CPU
// workers two, its 16-CPU worker eight, where a fixed three left the big
// node idle and oversubscribed the small ones (2026-10-15). An override in
// CLUSTARR_CONSUMER_SLOTS (chart markers.consumerSlots) still wins.
func TestMarkersSizesItsSlotsFromItsCPUs(t *testing.T) {
	dom, spec := markersAnalyze(t)
	override := map[string]int{events.ConsumerMarkersAnalyze: 3}
	for _, tc := range []struct {
		name               string
		overrides          map[string]int
		cpus, coresPerSlot int
		want               int
	}{
		{"a 4-CPU node", nil, 4, events.DefaultCoresPerSlot, 2},
		{"a 16-CPU node", nil, 16, events.DefaultCoresPerSlot, 8},
		{"a node past the ceiling", nil, 48, events.DefaultCoresPerSlot, events.PerNodeSlotsCeiling},
		{"a CPU limit of 1 (GOMAXPROCS 1)", nil, 1, events.DefaultCoresPerSlot, 1},
		{"an I/O-bound cluster's 4-CPU node", nil, 4, 1, 4},
		{"the override on a 16-CPU node", override, 16, events.DefaultCoresPerSlot, 3},
		{"the override on a 4-CPU node", override, 4, 1, 3},
	} {
		sub := subscription(dom, spec, tc.overrides, tc.cpus, tc.coresPerSlot)
		assert.Equal(t, tc.want, sub.MaxInFlight, tc.name)
		assert.Equal(t, spec.MaxAckPending, sub.MaxAckPending, "%s: the durable's cap is the topology's", tc.name)
	}
}

// The pod reports the slots it runs, not the topology's, so the manager
// reads each pod's own capacity whatever its node.
func TestThePresenceReportCarriesThePodsSlots(t *testing.T) {
	dom, spec := markersAnalyze(t)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	for _, tc := range []struct {
		pod       string
		overrides map[string]int
		cpus      int
		want      int
	}{
		{"markers-big", nil, 16, 8},
		{"markers-small", nil, 4, 2},
		{"markers-pinned", map[string]int{events.ConsumerMarkersAnalyze: 3}, 16, 3},
	} {
		sub := subscription(dom, spec, tc.overrides, tc.cpus, events.DefaultCoresPerSlot)
		w := presenceWriter(kv, tc.pod, "node-"+tc.pod, sub, nil)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()
		require.Eventually(t, func() bool {
			_, err := kv.Get(context.Background(), w.Key())
			return err == nil
		}, 5*time.Second, 10*time.Millisecond, tc.pod)
		e, err := kv.Get(context.Background(), w.Key())
		require.NoError(t, err)
		var p schema.AgentPresence
		require.NoError(t, json.Unmarshal(e.Value, &p))
		assert.Equal(t, agentdomain.Markers, p.Domain, tc.pod)
		assert.Equal(t, map[string]int{events.ConsumerMarkersAnalyze: tc.want}, p.Slots, tc.pod)
		assert.Equal(t, []string{events.ConsumerMarkersAnalyze}, p.Durables, tc.pod)
		cancel()
		require.NoError(t, <-done)
	}
}

// Each decoder gets the pod's CPUs shared by its slots, at least one: with
// automatic slots --cores-per-slot each, and past the slot ceiling a big
// node's spare CPUs go to the decoders. --decode-threads wins.
func TestDecodeThreadsSplitTheCPUsAcrossTheSlots(t *testing.T) {
	dom, spec := markersAnalyze(t)
	for _, tc := range []struct {
		name               string
		flag, cpus, cores  int
		overrides          map[string]int
		wantSlots, threads int
	}{
		{"a 4-CPU node", 0, 4, 2, nil, 2, 2},
		{"a 16-CPU node", 0, 16, 2, nil, 8, 2},
		{"a 5-CPU node keeps a CPU for the analysis", 0, 5, 2, nil, 2, 2},
		{"a 3-CPU node's one slot decodes on all three", 0, 3, 2, nil, 1, 3},
		{"a 32-CPU node past the ceiling", 0, 32, 2, nil, 8, 4},
		{"a slot a core", 0, 4, 1, nil, 4, 1},
		{"an override above the CPUs", 0, 4, 2, map[string]int{events.ConsumerMarkersAnalyze: 6}, 6, 1},
		{"--decode-threads wins", 3, 16, 2, nil, 8, 3},
	} {
		sub := subscription(dom, spec, tc.overrides, tc.cpus, tc.cores)
		assert.Equal(t, tc.wantSlots, sub.MaxInFlight, tc.name)
		assert.Equal(t, tc.threads, decodeThreads(tc.flag, sub.MaxInFlight, tc.cpus), tc.name)
	}
}

func TestACoresPerSlotBelowOneExitsMisconfigured(t *testing.T) {
	env := map[string]string{"NATS_URL": "nats://127.0.0.1:1", "POD_NAME": "markers-worker-0"}
	assert.Equal(t, worker.ExitMisconfigured, run([]string{"--cores-per-slot=0"}, func(k string) string { return env[k] }))
}
