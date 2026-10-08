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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// TestPlanNormalisesALegacyBlock is §2.5 "Legacy objects": a phase-less
// block a 0.4.x install stored reads as Succeeded when it carries a
// profileTag, else as absent; nothing else of it survives the first apply.
func TestPlanNormalisesALegacyBlock(t *testing.T) {
	legacy := &catalogv1alpha1.TranscodeState{
		Compliant: true, ProfileTag: "hevc@0ld", ProbeHash: "ph-0ld", Message: "transcoded by job heat-1995-0ld00000",
	}
	for _, grant := range []GrantKind{GrantNone, GrantWindow} {
		d := pass(inputs(legacy, probed("ph-1"), grant))
		require.NotNil(t, d.Block)
		assert.Equal(t, &catalogv1alpha1.TranscodeState{
			Phase: catalogv1alpha1.TranscodePhaseSucceeded, Compliant: true, ProfileTag: "hevc@0ld",
		}, d.Block)
	}

	tagless := &catalogv1alpha1.TranscodeState{Message: "the job failed"}
	assert.Nil(t, pass(inputs(tagless, probed("ph-1"), GrantNone)).Block)
}

// TestAVerdictHoldsOnlyForItsTagAndProbe is §5.11 "Verdicts" and D13: with no
// grant a verdict is never rewritten (admission reads a stale one as
// eligible without a write); under a window grant a stale one is planned
// again, a current one is not.
func TestAVerdictHoldsOnlyForItsTagAndProbe(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantNone)
	cur := &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSkipped, Reason: ReasonPlanSkip,
		Profile: "hevc", ProfileHash: in.Eligibility.Hash, ProfileTag: in.Eligibility.Tag, ProbeHash: "ph-1"}
	stale := cur.DeepCopy()
	stale.ProfileHash, stale.ProfileTag = "0ld", "hevc@0ld"
	failed := cur.DeepCopy()
	failed.Phase, failed.Reason, failed.Blocked = catalogv1alpha1.TranscodePhaseFailed, ReasonRetriesExhausted, true

	assert.Equal(t, cur, pass(inputs(cur, probed("ph-1"), GrantNone)).Block)
	assert.Equal(t, stale, pass(inputs(stale, probed("ph-1"), GrantNone)).Block, "untouched until the window takes it")
	assert.Equal(t, cur, pass(inputs(cur, probed("ph-2"), GrantNone)).Block, "new bytes make it eligible; the block waits for the window too")

	for name, b := range map[string]*catalogv1alpha1.TranscodeState{"a stale tag": stale, "new bytes": cur} {
		t.Run(name, func(t *testing.T) {
			probe := "ph-1"
			if name == "new bytes" {
				probe = "ph-2"
			}
			d := pass(inputs(b, probed(probe), GrantWindow))
			require.NotNil(t, d.Block)
			assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, d.Block.Phase)
			assert.Equal(t, probe, d.Block.ProbeHash)
			assert.Equal(t, GrantConfirm, d.Grant)
		})
	}
	for name, b := range map[string]*catalogv1alpha1.TranscodeState{"Skipped": cur, "Failed": failed} {
		d := pass(inputs(b, probed("ph-1"), GrantWindow))
		assert.Equal(t, GrantRelease, d.Grant, "a current %s verdict takes no place", name)
		assert.Equal(t, b, d.Block, "a current %s verdict is not rewritten", name)
	}
}

// TestAWindowGrantPlansFromTheStoredProbe is §5.8 "Window grant": Planned
// with plannedAt, profile, profileHash, profileTag, probeHash, the plan
// summary and attempts 0, the plan made by standard.Plan for the profile's
// hardware; a pending file takes its place as Pending.
func TestAWindowGrantPlansFromTheStoredProbe(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantWindow)
	d := pass(in)
	require.NotNil(t, d.Block)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, b.Phase)
	assert.Equal(t, ReasonWaitingForSlot, b.Reason)
	assert.Equal(t, "waiting for a free slot", b.Message)
	require.NotNil(t, b.PlannedAt)
	assert.Equal(t, metav1.NewTime(t0), *b.PlannedAt)
	assert.Equal(t, "hevc", b.Profile)
	assert.Equal(t, in.Eligibility.Hash, b.ProfileHash)
	assert.Equal(t, in.Eligibility.Tag, b.ProfileTag)
	assert.Equal(t, "ph-1", b.ProbeHash)
	assert.Equal(t, catalogv1alpha1.TranscodeHardware("auto"), b.Hardware, "an empty spec.hardware is the CRD default")
	assert.Zero(t, b.Attempts)
	assert.Nil(t, b.Dispatch)
	require.NotNil(t, b.Plan)
	assert.Equal(t, catalogv1alpha1.TranscodeModeTranscode, b.Plan.Mode)
	assert.Equal(t, "libx265", b.Plan.Encoder, "auto plans for cpu until a class is chosen")
	assert.NotEmpty(t, b.Plan.PlanHash)
	assert.Equal(t, GrantConfirm, d.Grant)

	// The plan is the standard's own for the stored summary.
	p, fail := PlanFor(source, in.Eligibility.Profile, in.Eligibility.Hash, in.File, in.Draft, nil, Device{})
	require.Nil(t, fail)
	assert.Equal(t, p.Plan.Hash(), b.Plan.PlanHash)

	pending := inputs(nil, probed("ph-1"), GrantWindow)
	pending.ProbeCurrent = false
	f := eligibleFile(pending.Draft)
	f.ProbeCurrent = false
	pending.Eligibility = Eligible(f, []transcodev1alpha1.TranscodeProfile{newProfile("hevc", true, t0, nil)})
	pd := pass(pending)
	require.NotNil(t, pd.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhasePending, pd.Block.Phase)
	assert.Equal(t, ReasonProbePending, pd.Block.Reason)
	assert.Nil(t, pd.Block.Plan, "nothing is planned from a probe of other bytes")
	assert.Equal(t, GrantConfirm, pd.Grant, "a Pending file holds its window place")

	gone := inputs(nil, probed("ph-1"), GrantWindow)
	gone.Eligibility.Eligible, gone.Eligibility.Why = false, "its movie is gone"
	gd := pass(gone)
	assert.Nil(t, gd.Block)
	assert.Equal(t, GrantRelease, gd.Grant)
}

// TestAWindowGrantOnAHeldFileIsSkipped: MP4 ruling R1 -- a file the standard
// holds (image subtitles) is a Skipped verdict for (profileHash, probeHash),
// so a standard.Version raise plans it again.
func TestAWindowGrantOnAHeldFileIsSkipped(t *testing.T) {
	st := probed("ph-1")
	st.MediaInfo.Subtitles = []commonv1.SubtitleStream{{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "eng", Bitmap: true}}
	in := inputs(nil, st, GrantWindow)
	d := pass(in)
	require.NotNil(t, d.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSkipped, d.Block.Phase)
	assert.Equal(t, ReasonPlanSkip, d.Block.Reason)
	assert.Contains(t, d.Block.Message, standard.HoldImageSubtitles)
	assert.Equal(t, "ph-1", d.Block.ProbeHash)
	assert.Equal(t, in.Eligibility.Hash, d.Block.ProfileHash)
	assert.Nil(t, d.Block.Plan, "a skip records no plan")
	assert.Equal(t, GrantConfirm, d.Grant)

	again := inputs(d.Block, st, GrantWindow)
	assert.Equal(t, GrantRelease, pass(again).Grant, "the verdict holds for its (profileHash, probeHash)")
}

// TestThePlanNamesTheSubtitlesItDrops is D44 (loop spec §2.5, §5.8):
// status.transcode.plan.dropped carries standard.Result.Dropped, where the
// operator reads the plan, and a plan that drops nothing carries none.
func TestThePlanNamesTheSubtitlesItDrops(t *testing.T) {
	drops := []string{`subtitle 3 (ass "Honorifics"): a second en.ass sidecar`}
	p := StatusPlan(&standard.Result{Decision: standard.DecisionEncode, Video: standard.VideoPlan{Encoder: "libx265"}, Dropped: drops})
	require.NotNil(t, p)
	assert.Equal(t, drops, p.Dropped)
	drops[0] = "changed"
	assert.NotEqual(t, drops, p.Dropped, "the status holds its own copy")
	assert.Empty(t, StatusPlan(&standard.Result{Decision: standard.DecisionEncode, Video: standard.VideoPlan{Encoder: "libx265"}}).Dropped)
	assert.Nil(t, StatusPlan(&standard.Result{Decision: standard.DecisionSkip, Dropped: drops}), "a skip records no plan")

	// Both of the standard's reasons: a second English SubRip track (a
	// second en.srt) and a dvb_teletext track, neither text nor bitmap (a
	// bitmap codec would hold the file, HoldImageSubtitles).
	st := probed("ph-1")
	st.MediaInfo.Subtitles = []commonv1.SubtitleStream{{Index: 2, Codec: "subrip", Language: "eng"},
		{Index: 3, Codec: "subrip", Language: "eng"}, {Index: 4, Codec: "dvb_teletext", Language: "ger"}}
	d := pass(inputs(nil, st, GrantWindow))
	require.NotNil(t, d.Block)
	require.NotNil(t, d.Block.Plan)
	require.Len(t, d.Block.Plan.Dropped, 2, "%q", d.Block.Plan.Dropped)
	assert.Contains(t, d.Block.Plan.Dropped[0], "a second", "a second English SubRip track has no sidecar name of its own")
	assert.Contains(t, d.Block.Plan.Dropped[1], "dvb_teletext")
}

// TestAPlannedFileLeavesTheWindowWhenItStopsBeingEligible is §5.11 "Leaving
// the window"; a profile edit does not evict a Planned file.
func TestAPlannedFileLeavesTheWindowWhenItStopsBeingEligible(t *testing.T) {
	planned := pass(inputs(nil, probed("ph-1"), GrantWindow)).Block
	require.NotNil(t, planned)
	require.Equal(t, catalogv1alpha1.TranscodePhasePlanned, planned.Phase)
	in := inputs(planned, probed("ph-1"), GrantNone)
	assert.Equal(t, planned, pass(in).Block, "an eligible Planned file keeps its place untouched")
	in.Eligibility.Eligible, in.Eligibility.Why = false, "item gone"
	assert.Nil(t, pass(in).Block)

	edited := planned.DeepCopy()
	edited.ProfileHash, edited.ProfileTag = "0ld", "hevc@0ld"
	assert.Equal(t, edited, pass(inputs(edited, probed("ph-1"), GrantNone)).Block, "re-planned at dispatch, not evicted")

	pending := planned.DeepCopy()
	pending.Phase, pending.Reason, pending.Plan, pending.PlannedAt = catalogv1alpha1.TranscodePhasePending, ReasonProbePending, nil, nil
	gone := inputs(pending, probed("ph-1"), GrantNone)
	gone.Eligibility = Eligibility{Why: "its movie is gone"}
	assert.Nil(t, pass(gone).Block, "a Pending file leaves the window too")
}

// TestASwapIsIncorporatedWithTheDispatchedTag is §5.9 "The swap": Swapping
// becomes Succeeded once the probe of the output lands; the tag is the
// block's, never the profile's current hash.
func TestASwapIsIncorporatedWithTheDispatchedTag(t *testing.T) {
	out := "/data/media/movies/Heat (1995)/Heat (1995).mp4"
	swapping := &catalogv1alpha1.TranscodeState{
		Phase: catalogv1alpha1.TranscodePhaseSwapping, Profile: "hevc", ProfileHash: "d1spatched", ProfileTag: "hevc@d1spatched",
		ProbeHash: "ph-1", FinishedAt: new(metav1.NewTime(t0)), Hardware: "auto", Class: "cpu",
		Dispatch:  &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: metav1.NewTime(t0.Add(-time.Hour))},
		Result:    &catalogv1alpha1.TranscodeOutput{OutputPath: out, OutputSizeBytes: 3 << 30},
	}
	notYet := inputs(swapping, probed("ph-1"), GrantNone)
	require.NotEqual(t, "d1spatched", notYet.Eligibility.Hash, "the profile's current hash is another")
	assert.Equal(t, swapping, pass(notYet).Block, "Swapping waits for the probe of its output")

	for name, at := range map[string]time.Time{"a second later": t0.Add(time.Second), "the same second": t0} {
		t.Run(name, func(t *testing.T) {
			landed := probed("ph-out")
			landed.ProbedAt = new(metav1.NewTime(at)) // the catalog's !finishedAt.After(probedAt)
			landed.MediaInfo.TranscodeProfile = "hevc@d1spatched"
			d := pass(inputs(swapping, landed, GrantNone))
			b := d.Block
			require.NotNil(t, b)
			assert.Equal(t, catalogv1alpha1.TranscodePhaseSucceeded, b.Phase)
			assert.True(t, b.Compliant)
			assert.Equal(t, "hevc@d1spatched", b.ProfileTag)
			assert.Equal(t, "ph-out", b.ProbeHash, "the verdict describes the output's bytes")
			assert.Nil(t, b.Result, "a swapped-in output is the file now; only a kept copy keeps result")
			assert.Equal(t, int64(7), b.Dispatch.Seq)
			assert.Equal(t, []string{"Succeeded"}, reasons(d.Events))
		})
	}

	early := probed("ph-out")
	early.ProbedAt = new(metav1.NewTime(t0.Add(-time.Second)))
	early.MediaInfo.TranscodeProfile = "hevc@d1spatched"
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, pass(inputs(swapping, early, GrantNone)).Block.Phase,
		"a probe from before the worker finished is the source's")

	wrong := probed("ph-out")
	wrong.ProbedAt = new(metav1.NewTime(t0.Add(time.Second)))
	wrong.MediaInfo.TranscodeProfile = "other@abc"
	d := pass(inputs(swapping, wrong, GrantNone))
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseFailed, b.Phase)
	assert.True(t, b.Blocked)
	assert.Equal(t, ReasonInvalidSource, b.Reason)
	assert.Contains(t, b.Message, `"other@abc"`)
	assert.Equal(t, []string{ReasonInvalidSource}, reasons(d.Events))
}

// TestNewBytesOnASucceededFileDropItsVerdict is staleTranscodeState's rule,
// moved from the MediaFile controller: compliance and the tag described bytes
// that are gone; the block stays Succeeded and spec.original keeps the file
// Transcoded().
func TestNewBytesOnASucceededFileDropItsVerdict(t *testing.T) {
	done := &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, Compliant: true,
		ProfileTag: "hevc@d1spatched", ProbeHash: "ph-out"}
	assert.Equal(t, done, pass(inputs(done, probed("ph-out"), GrantNone)).Block)
	b := pass(inputs(done, probed("ph-remuxed"), GrantNone)).Block
	assert.Equal(t, &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProbeHash: "ph-out"}, b)
	assert.Equal(t, b, pass(inputs(b, probed("ph-remuxed"), GrantNone)).Block, "dropped once, then kept")
}

// TestARenameKeepsASucceededFilesVerdict: the probe hash hashes the path, so
// renaming a transcoded file to its canonical name moves it, but the bytes
// still carry the swap's CLUSTARR_PROFILE. The verdict follows the file; on
// kind a rename after the swap dropped profileTag and compliant (smoke test,
// 2026-10-07), and a block that lost them that way gets them back.
func TestARenameKeepsASucceededFilesVerdict(t *testing.T) {
	done := &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, Compliant: true,
		Profile: "hevc", ProfileHash: "d1spatched", ProfileTag: "hevc@d1spatched", ProbeHash: "ph-out"}
	renamed := probed("ph-renamed")
	renamed.MediaInfo.TranscodeProfile = "hevc@d1spatched"

	want := done.DeepCopy()
	want.ProbeHash = "ph-renamed"
	assert.Equal(t, want, pass(inputs(done, renamed, GrantNone)).Block)

	dropped := done.DeepCopy()
	dropped.Compliant, dropped.ProfileTag = false, ""
	assert.Equal(t, want, pass(inputs(dropped, renamed.DeepCopy(), GrantNone)).Block, "a dropped verdict is restored")

	other := probed("ph-renamed")
	other.MediaInfo.TranscodeProfile = "other@abc"
	gone := done.DeepCopy()
	gone.Compliant, gone.ProfileTag = false, ""
	assert.Equal(t, gone, pass(inputs(done, other, GrantNone)).Block, "another tag is new bytes")
}
