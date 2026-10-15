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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
)

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
	spec, ok := events.Default().Consumer(events.ConsumerMarkersAnalyze)
	require.True(t, ok)
	sub := subscription(spec, map[string]int{events.ConsumerMarkersAnalyze: 3})
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
