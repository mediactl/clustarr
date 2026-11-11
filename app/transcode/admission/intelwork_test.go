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

package admission

import (
	"testing"

	"github.com/stretchr/testify/assert"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// The intel class's work in hand and waiting (cluster-plex's 2026-11-10
// CPU video route design, §1): running, queued and Planned intel files and
// the yield's held gpu files; never grafts, cpu or nvidia, and nothing at
// all without an intel budget.
func TestIntelWorkCountsTheIntelClassInHandAndWaiting(t *testing.T) {
	intelSlot := func(k string) Slot { return Slot{Key: k, Hardware: string(intel)} }
	running := []Slot{intelSlot("a"), intelSlot("b"), {Key: "c", Hardware: "cpu"}, {Key: "d", Hardware: "nvidia"}}
	queued := []candidate{{slot: intelSlot("e")}, {slot: Slot{Key: "f", Hardware: "cpu"}}}
	holds := []hold{{msg: WaitingForGPU}, {msg: "waiting: the pool is unhealthy"}}
	budget := map[string]int32{"intel": 2, "cpu": 2}
	assert.Equal(t, 4, intelWork(queued, running, holds, budget), "two running, one queued, one held gpu file")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"cpu": 2}), "no intel budget is no intel work")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"intel": 0}))
}

func TestNoIntelBudgetIsNoIntelWork(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 2}, 0)
	gpu := fv("g", 1, planned)
	gpu.Hardware = transcodev1alpha1.HardwareGPU
	l.Observe(gpu)
	res := l.Pass(passIn())
	assert.Zero(t, res.IntelWork)
}

func TestAPassReportsItsIntelWork(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	r := fv("r", 1, running)
	r.Class = intel
	l.Observe(r)
	l.Observe(fv("a", 2, planned)) // auto: competes for intel on an Intel node
	j := fv("j", 3, planned)
	j.Hardware = intel // pinned intel, beyond the budget this pass
	l.Observe(j)
	res := l.Pass(intelIn(0))
	assert.Equal(t, 1, l.dispatchGrants(intel), "one slot is left beside the running encode")
	assert.Equal(t, 3, res.IntelWork, "the running encode, the admitted file and the one still waiting")

	held, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	g := fv("g", 1, planned)
	g.Hardware = transcodev1alpha1.HardwareGPU
	held.Observe(g)
	res = held.Pass(intelIn(1))
	assert.Equal(t, YieldingToPlex, held.Snapshot("g").WaitReason)
	assert.Equal(t, 1, res.IntelWork, "a gpu file the yield holds is intel work waiting")
}
