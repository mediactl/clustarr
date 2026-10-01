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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
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
	p := statusPlan(&transcode.PlanResult{Decision: transcode.DecisionRemuxOnly, Tier: transcode.TierNVENC, VideoArgs: []string{"-c:v", "copy"}})
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

// Under a GPU pin, a source only the CPU can encode -- Dolby Vision, which
// the planner never gives a hardware encoder -- is Skipped with a reason,
// never dispatched to cpu and never left Planned. The result comes from the
// real planner.
func TestAPinnedGPUPlanThatNeedsTheCPUIsSkipped(t *testing.T) {
	maxRate, bufSize := int32(20000), int32(40000)
	profile := transcode.ProfileSpec{
		Container: transcode.ContainerMKV, Hardware: transcode.HardwareNVIDIA,
		Video: transcode.VideoSpec{
			Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10",
			CRF: transcode.CRFTable{SD: 21, HD: 22, UHD: 23}, Preset: "slow",
			KeyintFactor: 10, BFrames: 4, Refs: 4, RCLookahead: 40, AQMode: 3,
			MaxRateKbps: &maxRate, BufSizeKbps: &bufSize,
			NVENC: transcode.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"},
		},
		Audio: transcode.AudioSpec{Codec: "aac", BitratePerChannelKbps: 64},
		HDR:   transcode.HDRSpec{DolbyVision: transcode.DolbyVisionPassthrough},
	}
	dv := transcode.MediaInfo{
		Path:   "/data/media/movies/M (2020)/M (2020).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", Profile: "High", PixFmt: "yuv420p", Width: 1920, Height: 1080,
			FrameRate: transcode.Rational{Num: 24, Den: 1},
			HDR:       transcode.HDRInfo{Format: commonv1.HdrFormatDolbyVision, DolbyVision: &mediainfo.DoviRecord{Profile: 9}},
		}},
		Audio: []transcode.AudioStream{{Codec: "truehd", Channels: 8, Language: "eng"}},
	}
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierNVENC: true, transcode.TierCPUx265: true}}
	res, err := transcode.Plan(dv, profile, caps, transcode.PlanMeta{ProfileName: "p", ProfileHash: "h", Threads: 1})
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, res.Decision)
	require.Equal(t, "libx265", encoderName(res), "the planner never gives Dolby Vision a hardware encoder")

	pinned := &transcodev1alpha1.TranscodeProfile{Spec: transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA}}
	got := skipCPUPlanUnderGPUPin(&transcodev1alpha1.TranscodeJob{}, pinned, planning{result: res})
	assert.Equal(t, transcode.DecisionSkip, got.result.Decision)
	assert.Contains(t, got.result.Reason, "nvidia")
	assert.Equal(t, transcode.DecisionEncode, res.Decision, "the planner's result is not mutated")

	auto := &transcodev1alpha1.TranscodeProfile{Spec: transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareAuto}}
	assert.Equal(t, transcode.DecisionEncode, skipCPUPlanUnderGPUPin(&transcodev1alpha1.TranscodeJob{}, auto, planning{result: res}).result.Decision,
		"an auto job's CPU plan goes to cpu as before")
}

// A standard plan the GPU pin turns into a skip is recorded as a skip, not
// as the encode it was.
func TestAPinnedStandardSkipRecordsASkipPlan(t *testing.T) {
	res := &transcode.PlanResult{Decision: transcode.DecisionEncode, Tier: transcode.TierCPUx265}
	std := &standard.Result{Decision: standard.DecisionEncode, Reason: "encode to HEVC Main 10",
		Video: standard.VideoPlan{Action: "encode", Encoder: "libx265", Decode: "cpu"}}
	pinned := &transcodev1alpha1.TranscodeProfile{Spec: transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA}}
	got := skipCPUPlanUnderGPUPin(&transcodev1alpha1.TranscodeJob{}, pinned, planning{result: res, std: std})
	require.Equal(t, transcode.DecisionSkip, got.result.Decision)
	plan := statusPlanOf(got)
	assert.Equal(t, transcodev1alpha1.PlanModeSkip, plan.Mode)
	assert.Empty(t, plan.Encoder)
	assert.Contains(t, plan.SkipReason, "nvidia")
	assert.Equal(t, standard.DecisionEncode, std.Decision, "the standard's result is not mutated")
}

func ptrTo[T any](v T) *T { return &v }
