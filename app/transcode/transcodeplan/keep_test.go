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

package transcodeplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// A swap whose worker kept the video -- its encode came out no smaller
// than the source, so it remuxed instead (standard.KeepVideo) -- records
// that remux as status.transcode.plan: videoAction keep, the remux's plan
// hash, the encode's dropped tracks, and a message that says why.
func TestASwapThatKeptTheVideoRecordsTheRemux(t *testing.T) {
	for _, phase := range []catalogv1alpha1.TranscodePhase{
		catalogv1alpha1.TranscodePhaseSwapping, catalogv1alpha1.TranscodePhaseSucceeded,
	} {
		t.Run(string(phase), func(t *testing.T) {
			b := catalogv1alpha1.TranscodeState{
				Profile: "hevc", ProfileHash: "h1",
				Plan: &catalogv1alpha1.TranscodePlan{
					Mode: catalogv1alpha1.TranscodeModeTranscode, Encoder: "hevc_qsv", VideoAction: standard.VideoEncoded,
					Decode: "vaapi", HDRMode: "sdr", PlanHash: "encode-hash", Dropped: []string{"subtitle 3 (pgs): dvd"},
				},
			}
			a := task.Answer{Outcome: task.OutcomeSucceeded, Result: &task.Result{
				OutputPath: source, OutputSizeBytes: 900, OutputToSourcePercent: 90,
				KeptVideo: &task.KeptVideo{PlanHash: "keep-hash", EncodePercent: 110},
			}}
			applyAnswer(&b, a, Next{Phase: phase}, t0)

			require.NotNil(t, b.Plan)
			assert.Equal(t, catalogv1alpha1.TranscodePlan{
				Mode: catalogv1alpha1.TranscodeModeRemuxOnly, Encoder: "copy", VideoAction: standard.VideoKept,
				PlanHash: "keep-hash", Dropped: []string{"subtitle 3 (pgs): dvd"},
			}, *b.Plan)
			assert.Contains(t, b.Message, "the encode came out 110% of the source, so its video was kept")
			assert.Equal(t, "remux to MP4, video kept (its encode was no smaller)", b.Plan.Describe())
		})
	}

	b := catalogv1alpha1.TranscodeState{Plan: &catalogv1alpha1.TranscodePlan{
		Mode: catalogv1alpha1.TranscodeModeTranscode, Encoder: "hevc_qsv", VideoAction: standard.VideoEncoded, PlanHash: "e",
	}}
	applyAnswer(&b, task.Answer{Outcome: task.OutcomeSucceeded, Result: &task.Result{OutputPath: source}},
		Next{Phase: catalogv1alpha1.TranscodePhaseSwapping}, t0)
	assert.Equal(t, standard.VideoEncoded, b.Plan.VideoAction, "an encode that fit keeps its plan")
	assert.Equal(t, "transcode succeeded; waiting for the output's probe", b.Message)
}

// A tagged H.264 file whose recorded transcode kept the video is final
// under a profile that no longer keeps H.264: KeptH264Dropped encodes a
// remux that copied H.264, never one whose encode was already no smaller,
// and the plan the planner would record reads the kept tag as the worker's
// probe of the file does.
func TestAKeptH264VideoIsNeverPlannedAgain(t *testing.T) {
	st := probed("h1")
	st.MediaInfo.TranscodeProfile = "hevc@old"
	st.MediaInfo.Container = "mov,mp4,m4a,3gp,3g2,mj2"
	st.Transcode = &catalogv1alpha1.TranscodeState{
		Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProfileTag: "hevc@old",
		Plan: &catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeRemuxOnly, VideoAction: standard.VideoKept},
	}
	f := eligibleFile(st)
	f.MF.Spec.Original = new(false)
	assert.Equal(t, ReasonTranscoded, Eligible(f, hevcProfiles()).Why, "kept, the remux is final")

	info, err := MediaInfoOf(source, f.MF, st, "hevc@new", "")
	require.NoError(t, err)
	assert.Equal(t, standard.VideoKept, info.Tags[standard.VideoTagKey])

	st.Transcode.Plan.VideoAction = standard.VideoCopied
	assert.True(t, Eligible(eligibleFile(st), hevcProfiles()).Eligible, "a remux kept only by policy.keepCodecs is encoded once")
}

// A swap the loop recovered before the worker's answer landed (talos,
// 2026-11-16: Spare Me, Great Lord! S01E11) records only the dispatched
// encode. The output's probe says the video was kept: the incorporation
// rewrites the plan to the remux that ran, and the file is final -- where
// the planner had dispatched the encode again at once, which the worker
// refused on the file's own CLUSTARR_VIDEO=keep.
func TestARecoveredSwapOfAKeptVideoIsFinal(t *testing.T) {
	swapping := &catalogv1alpha1.TranscodeState{
		Phase: catalogv1alpha1.TranscodePhaseSwapping, Profile: "hevc", ProfileHash: "d1spatched", ProfileTag: "hevc@d1spatched",
		ProbeHash: "ph-1", FinishedAt: new(metav1.NewTime(t0)), Hardware: "auto", Class: "intel",
		Dispatch: &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: metav1.NewTime(t0.Add(-time.Hour))},
		Plan: &catalogv1alpha1.TranscodePlan{
			Mode: catalogv1alpha1.TranscodeModeTranscode, Encoder: "hevc_qsv", VideoAction: standard.VideoEncoded, PlanHash: "enc",
		},
	}
	landed := probed("ph-out")
	landed.ProbedAt = new(metav1.NewTime(t0.Add(time.Second)))
	landed.MediaInfo.TranscodeProfile, landed.MediaInfo.TranscodeVideo = "hevc@d1spatched", standard.VideoKept
	landed.MediaInfo.Container = "mov,mp4,m4a,3gp,3g2,mj2"
	d := pass(inputs(swapping, landed, GrantNone))
	require.NotNil(t, d.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSucceeded, d.Block.Phase)
	require.NotNil(t, d.Block.Plan)
	assert.Equal(t, standard.VideoKept, d.Block.Plan.VideoAction)
	assert.Equal(t, catalogv1alpha1.TranscodeModeRemuxOnly, d.Block.Plan.Mode)
	assert.Equal(t, "enc", d.Block.Plan.PlanHash, "the remux's own hash is unknown here")
	assert.Contains(t, d.Block.Message, "its video was kept")

	// The record still says encode (as before the incorporation): the probe
	// alone keeps the file final.
	landed.Transcode = &catalogv1alpha1.TranscodeState{
		Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProfileTag: "hevc@d1spatched", Plan: swapping.Plan.DeepCopy(),
	}
	f := eligibleFile(landed)
	f.MF.Spec.Original = new(false)
	assert.Equal(t, ReasonTranscoded, Eligible(f, hevcProfiles()).Why)
	assert.Equal(t, standard.VideoKept, VideoActionOf(landed), "the probe beats the record")
}
