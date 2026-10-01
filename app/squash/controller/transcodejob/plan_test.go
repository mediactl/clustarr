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
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func TestHardwareForEncoder(t *testing.T) {
	for enc, want := range map[string]transcodev1alpha1.Hardware{
		"libx265": "cpu", "copy": "cpu", "hevc_nvenc": "nvidia", "hevc_qsv": "intel", "hevc_vaapi": "intel", "": "cpu",
	} {
		assert.Equal(t, want, hardwareForEncoder(enc), enc)
	}
}

func TestStatusPlanRemuxTakesACPUSlot(t *testing.T) {
	p := statusPlanOf(planning{plan: standard.Result{
		Decision: standard.DecisionCopyVideo,
		Video:    standard.VideoPlan{Action: "copy"},
	}})
	assert.Equal(t, transcodev1alpha1.PlanModeRemuxOnly, p.Mode)
	assert.Equal(t, "copy", p.Encoder)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, hardwareForEncoder(p.Encoder))
}

// classFor is the plan's encoder's class, and CPU once a fallback reason is
// recorded or when there is no plan to read one from (spec §18.5). It is the
// class of a pinned job, or an auto one whose plan encodes nothing; an auto
// job that encodes is given ChooseClass's (TestAssignClasses).
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

// A job pinned to a GPU class (its profile's spec.hardware, or its own)
// dispatches only to that class: a remux copies the video, so it runs in
// the GPU pool too, rather than creating a CPU pool the pin excludes. An
// auto job's remux still takes a CPU slot.
func TestClassForAPinnedGPUJob(t *testing.T) {
	r := &Reconciler{}
	pinned := &transcodev1alpha1.TranscodeProfile{Spec: transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA}}
	auto := &transcodev1alpha1.TranscodeProfile{Spec: transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareAuto}}
	tj := func(mode transcodev1alpha1.PlanMode, encoder string) *transcodev1alpha1.TranscodeJob {
		out := &transcodev1alpha1.TranscodeJob{}
		if encoder != "" {
			out.Status.Plan = &transcodev1alpha1.Plan{Mode: mode, Encoder: encoder}
		}
		return out
	}
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, r.classFor(tj(transcodev1alpha1.PlanModeRemuxOnly, "copy"), pinned))
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, r.classFor(tj("", ""), pinned), "no plan yet: still the pinned class")
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, r.classFor(tj(transcodev1alpha1.PlanModeTranscode, "hevc_nvenc"), pinned))
	assert.Equal(t, transcodev1alpha1.HardwareCPU, r.classFor(tj(transcodev1alpha1.PlanModeRemuxOnly, "copy"), auto))

	own := tj(transcodev1alpha1.PlanModeRemuxOnly, "copy")
	own.Spec.Hardware = ptrTo(transcodev1alpha1.HardwareIntel)
	assert.Equal(t, transcodev1alpha1.HardwareIntel, r.classFor(own, auto), "the job's own pin wins over its profile's")
}

// A plan that encodes no video runs in any class's pool; one that encodes
// runs only in its encoder's.
func TestPlanRunsIn(t *testing.T) {
	remux := &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeRemuxOnly, Encoder: "copy"}
	nvenc := &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeTranscode, Encoder: "hevc_nvenc"}
	x265 := &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeTranscode, Encoder: "libx265"}
	assert.True(t, planRunsIn(remux, transcodev1alpha1.HardwareNVIDIA))
	assert.True(t, planRunsIn(remux, transcodev1alpha1.HardwareCPU))
	assert.True(t, planRunsIn(nvenc, transcodev1alpha1.HardwareNVIDIA))
	assert.False(t, planRunsIn(nvenc, transcodev1alpha1.HardwareCPU))
	assert.False(t, planRunsIn(x265, transcodev1alpha1.HardwareNVIDIA))
	assert.False(t, planRunsIn(nil, transcodev1alpha1.HardwareNVIDIA))
}

func ptrTo[T any](v T) *T { return &v }
