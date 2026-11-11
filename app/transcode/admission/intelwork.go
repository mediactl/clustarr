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
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// intelWork is the intel class's work in hand and waiting after a pass:
// its slots in use (running), the candidates assigned to it this pass
// (queued, admitted or not) and the holds of gpu files waiting class-less
// for a GPU slot (WaitingForGPU), which the yield holds beside Plex's
// streams. Nothing without an intel budget: a cluster that never admits
// to intel has no intel work however many files wait. cluster-plex reads
// the count from the Lease the admission controller publishes
// (IntelWorkLease) to send Plex's conversions to its CPU pool while it is
// above zero (its 2026-11-10 CPU video route design, §1).
func intelWork(queued []candidate, running []Slot, holds []hold, budget map[string]int32) int {
	if budget[string(transcodev1alpha1.HardwareIntel)] <= 0 {
		return 0
	}
	n := 0
	for _, s := range running {
		if s.Hardware == string(transcodev1alpha1.HardwareIntel) {
			n++
		}
	}
	for _, c := range queued {
		if c.slot.Hardware == string(transcodev1alpha1.HardwareIntel) {
			n++
		}
	}
	for _, h := range holds {
		if h.msg == WaitingForGPU {
			n++
		}
	}
	return n
}
