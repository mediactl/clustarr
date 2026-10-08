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
	"github.com/mediactl/clustarr/app/transcode/task"
)

// TestChooseClassGPUOnlyWaitsWhileAGPUIsUsable is spec §18.5's class choice:
// nvidia, then intel, then cpu, a fallback reason pinning cpu; and
// hardware: gpu (2026-10-01) answering "" -- wait -- rather than cpu while a
// GPU class could take the file, but cpu once none can.
func TestChooseClassGPUOnlyWaitsWhileAGPUIsUsable(t *testing.T) {
	nv, in, cpu := transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareCPU
	both := map[transcodev1alpha1.Hardware]bool{nv: true, in: true}
	slots := map[transcodev1alpha1.Hardware]int32{nv: 1, in: 1, cpu: 2}
	for _, tc := range []struct {
		name           string
		nodes, unsched map[transcodev1alpha1.Hardware]bool
		free           map[transcodev1alpha1.Hardware]int32
		fallback       bool
		gpuOnly        bool
		want           transcodev1alpha1.Hardware
	}{
		{"nvidia first", both, nil, slots, false, false, nv},
		{"intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, false, in},
		{"cpu when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, false, cpu},
		{"cpu without a GPU node", nil, nil, slots, false, false, cpu},
		{"intel without an nvidia node", map[transcodev1alpha1.Hardware]bool{in: true}, nil, slots, false, false, in},
		{"skip an unschedulable pool", both, map[transcodev1alpha1.Hardware]bool{nv: true}, slots, false, false, in},
		{"a fallback reason pins cpu", both, nil, slots, true, false, cpu},
		{"an over-budget class is full, not free", both, nil, map[transcodev1alpha1.Hardware]int32{nv: -1, in: 0}, false, false, cpu},
		// hardware: gpu (2026-10-01): any GPU, never the CPU for want of a
		// slot -- "" is "wait" -- but the CPU when no GPU class can be used
		// at all, or a GPU already refused the file (its fallback reason).
		{"gpu: nvidia first", both, nil, slots, false, true, nv},
		{"gpu: intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, true, in},
		{"gpu: waits when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, true, ""},
		{"gpu: waits for a full intel when nvidia is unschedulable", both, map[transcodev1alpha1.Hardware]bool{nv: true}, map[transcodev1alpha1.Hardware]int32{nv: 1, cpu: 2}, false, true, ""},
		{"gpu: cpu without a GPU node", nil, nil, slots, false, true, cpu},
		{"gpu: cpu when every GPU pool is unschedulable", both, both, slots, false, true, cpu},
		{"gpu: a fallback reason pins cpu", both, nil, slots, true, true, cpu},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseClass(tc.nodes, tc.unsched, tc.free, tc.fallback, tc.gpuOnly))
		})
	}
}

// TestAClassIsUnhealthyOnlyWhenEveryReportSaysSo: one healthy node keeps a
// class, since work may land on it; no report is not unhealthy.
func TestAClassIsUnhealthyOnlyWhenEveryReportSaysSo(t *testing.T) {
	_, ok := UnhealthyMessage(transcodev1alpha1.HardwareNVIDIA, nil)
	assert.False(t, ok, "no report")
	_, ok = UnhealthyMessage(transcodev1alpha1.HardwareNVIDIA, map[string]task.NodeHealth{
		"a": {Healthy: false, Error: "no device"}, "b": {Healthy: true},
	})
	assert.False(t, ok, "one node can still take work")
	msg, ok := UnhealthyMessage(transcodev1alpha1.HardwareNVIDIA, map[string]task.NodeHealth{
		"b": {Healthy: false, Error: "busy"}, "a": {Healthy: false, Error: "no device"},
	})
	assert.True(t, ok)
	assert.Contains(t, msg, "a: no device", "the first node by name, with its reason")
	assert.Contains(t, msg, "nvidia", "the class it names")
}
