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

	"github.com/mediactl/clustarr/pkg/events"
)

func TestHandlerBudgetFallsBackToAckWait(t *testing.T) {
	assert.Equal(t, time.Minute, events.ConsumerSpec{AckWait: time.Minute}.HandlerBudget())
	assert.Equal(t, time.Hour, events.ConsumerSpec{AckWait: time.Minute, HandlerTimeout: time.Hour}.HandlerBudget())
	assert.Zero(t, events.ConsumerSpec{}.HandlerBudget())
}

// TestOnlyExplicitBudgetsAreEnforced: Subscription() copies the explicit
// HandlerTimeout, never the AckWait fallback. fileimport (60 s AckWait) and the
// rescan heartbeat past their AckWait by design; a budget there would cancel a
// cross-filesystem copy or a library walk (split §3.5.6 as amended). The
// explicit budgets are segment analysis's TaskTimeout, the manager's two
// intake durables' 60 s (ADR-0019, A1.6's table) and an engine's 2 m.
func TestOnlyExplicitBudgetsAreEnforced(t *testing.T) {
	budgets := map[string]time.Duration{
		events.ConsumerMarkersAnalyze:  30 * time.Minute,
		events.ConsumerIntakeCandidate: 60 * time.Second,
		events.ConsumerIntakeScan:      60 * time.Second,
	}
	seen := map[string]bool{}
	for _, c := range events.Default().Consumers {
		sub := c.Subscription()
		assert.Equal(t, c.HandlerTimeout, sub.HandlerTimeout, c.Name)
		if want, ok := budgets[c.Name]; ok {
			seen[c.Name] = true
			assert.Equal(t, want, c.HandlerTimeout, "%s: its budget", c.Name)
			continue
		}
		assert.Zero(t, c.HandlerTimeout, "%s: set a budget only from its clustarr_work_duration_seconds tail", c.Name)
	}
	assert.Len(t, seen, len(budgets), "every budgeted consumer is in the topology")
	c := events.TranscodeTaskConsumer("6f1c-uid", "cpu")
	assert.Zero(t, c.Subscription().HandlerTimeout, "%s: the pool worker heartbeats its own work", c.Name)
	// The engine never did: its commands lapsed at BackOff[0] while a slow
	// payload RPC ran, and the broker handed out second copies mid-run
	// (talos, 2026-10-15). Its budget covers the usenet engine's 60 s
	// resolve (TestAPayloadResolveFitsTheCommandBudget).
	e := events.EngineConsumer("qbit", 0)
	assert.Equal(t, 2*time.Minute, e.Subscription().HandlerTimeout, "%s: the bus heartbeats the engine's commands", e.Name)
}
