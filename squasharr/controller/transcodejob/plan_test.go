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

package transcodejob

import (
	"testing"

	"github.com/stretchr/testify/assert"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestHardwareForEncoder(t *testing.T) {
	for enc, want := range map[string]transcodev1alpha1.Hardware{
		"libx265": "cpu", "copy": "cpu", "hevc_nvenc": "nvidia", "hevc_qsv": "intel", "hevc_vaapi": "intel", "": "cpu",
	} {
		assert.Equal(t, want, hardwareForEncoder(enc), enc)
	}
}

func TestStatusPlanRemuxTakesACPUSlot(t *testing.T) {
	p := statusPlan(&transcode.PlanResult{Decision: transcode.DecisionRemuxOnly, Tier: transcode.TierNVENC, VideoArgs: []string{"-c:v", "copy"}})
	assert.Equal(t, transcodev1alpha1.PlanModeRemuxOnly, p.Mode)
	assert.Equal(t, "copy", p.Encoder)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, hardwareForEncoder(p.Encoder))
}

// classFor is the plan's encoder's class, and CPU once a fallback reason is
// recorded or when there is no plan to read one from (spec §18.5; Task 13
// makes auto capacity-aware).
func TestClassFor(t *testing.T) {
	r := &Reconciler{}
	tj := func(encoder, fallback string) *transcodev1alpha1.TranscodeJob {
		out := &transcodev1alpha1.TranscodeJob{}
		if encoder != "" {
			out.Status.Plan = &transcodev1alpha1.Plan{Encoder: encoder}
		}
		out.Status.FallbackReason = fallback
		return out
	}
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, r.classFor(tj("hevc_nvenc", ""), nil))
	assert.Equal(t, transcodev1alpha1.HardwareIntel, r.classFor(tj("hevc_qsv", ""), nil))
	assert.Equal(t, transcodev1alpha1.HardwareCPU, r.classFor(tj("copy", ""), nil), "a remux takes a CPU slot")
	assert.Equal(t, transcodev1alpha1.HardwareCPU, r.classFor(tj("hevc_nvenc", "GPU attempt 1 failed"), nil),
		"a recorded fallback keeps the job on CPU")
	assert.Equal(t, transcodev1alpha1.HardwareCPU, r.classFor(tj("", ""), nil))
}
