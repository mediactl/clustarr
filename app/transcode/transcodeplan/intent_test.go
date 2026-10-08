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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestIntentsAreRecordedOnce is §2.9 "Handling": every one-shot nonce is
// recorded on the pass that sees it, with NothingToRetry or NothingToCancel
// when there is nothing to act on; an invalid value is recorded (as its
// HandledNonce) with InvalidAnnotation; a held file keeps its nonce pending.
func TestIntentsAreRecordedOnce(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantNone)
	in.Intents = Intents{Retry: "1759752000", Cancel: "1759752001"}
	d := Plan(in)
	require.NotNil(t, d.Nonces)
	assert.Equal(t, "1759752000", d.Nonces.TranscodeRetry)
	assert.Equal(t, "1759752001", d.Nonces.TranscodeCancel)
	assert.ElementsMatch(t, []string{ReasonNothingToRetry, ReasonNothingToCancel}, reasons(d.Events))

	in.Nonces = *d.Nonces
	again := Plan(in)
	assert.Nil(t, again.Nonces, "nothing pending: the stored nonces are kept")
	assert.Empty(t, again.Events, "a handled nonce is not handled again")

	in.Intents.Retry = "not a nonce!"
	bad := Plan(in)
	require.NotNil(t, bad.Nonces)
	assert.Equal(t, catalogv1alpha1.HandledNonce("not a nonce!"), bad.Nonces.TranscodeRetry, "an invalid one-shot is recorded")
	assert.Equal(t, "1759752001", bad.Nonces.TranscodeCancel, "the other leaf is kept")
	assert.Equal(t, []string{ReasonInvalidAnnotation}, reasons(bad.Events))
	in.Nonces = *bad.Nonces
	assert.Empty(t, Plan(in).Events, "an invalid value warns once")

	held := inputs(nil, probed("ph-1"), GrantNone)
	held.ProbeCurrent = false
	held.Intents.Retry = "1759752009"
	assert.Nil(t, Plan(held).Nonces, "a nonce on a held file waits for the hold to lift")
	grafting := inputs(nil, probed("ph-1"), GrantNone)
	grafting.Eligibility.Pending = ReasonGrafting
	grafting.Intents.Cancel = "1759752010"
	assert.Nil(t, Plan(grafting).Nonces, "a standalone graft holds the nonce too")
}

// TestRetryClearsAVerdictForTheCurrentTag is D14: retry clears a Failed or
// Skipped verdict for the current profile, so the file is backlog again and
// the window takes it with attempts 0; a verdict for another tag is left to
// the window, with NothingToRetry.
func TestRetryClearsAVerdictForTheCurrentTag(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantNone)
	blocked := &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseFailed, Blocked: true, Reason: ReasonRetriesExhausted,
		Profile: "hevc", ProfileHash: in.Eligibility.Hash, ProfileTag: in.Eligibility.Tag, ProbeHash: "ph-1", Attempts: 5,
		Dispatch: &catalogv1alpha1.Dispatch{Seq: 77, AnsweredSeq: 77, DispatchedAt: metav1.NewTime(t0)}}
	in.Prev, in.Intents.Retry = blocked, "1759752000"
	d := Plan(in)
	assert.Nil(t, d.Block, "the verdict goes; the file is backlog again")
	require.NotNil(t, d.Nonces)
	assert.Equal(t, "1759752000", d.Nonces.TranscodeRetry)
	assert.Equal(t, []string{ReasonRetried}, reasons(d.Events))

	in.Nonces = *d.Nonces
	assert.Equal(t, blocked, Plan(in).Block, "a handled nonce retries nothing")

	window := inputs(blocked, probed("ph-1"), GrantWindow)
	window.Intents.Retry = "1759752001"
	w := Plan(window)
	require.NotNil(t, w.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, w.Block.Phase, "the window takes the retried file at once")
	assert.Zero(t, w.Block.Attempts)
	assert.False(t, w.Block.Blocked)
	assert.Equal(t, GrantConfirm, w.Grant)

	stale := blocked.DeepCopy()
	stale.ProfileHash, stale.ProfileTag = "0ld", "hevc@0ld"
	other := inputs(stale, probed("ph-1"), GrantNone)
	other.Intents.Retry = "1759752002"
	o := Plan(other)
	assert.Equal(t, stale, o.Block, "a verdict for another tag is not this profile's to retry")
	assert.Equal(t, []string{ReasonNothingToRetry}, reasons(o.Events))
}

// TestStandingIntentsAreRecordedInTheBlock (§2.9): priority and hardware
// are recorded on a window block and apply at the next admission and
// dispatch; suspend holds a Planned file in its window place; an invalid
// value is ignored with InvalidAnnotation, the profile's value standing.
func TestStandingIntentsAreRecordedInTheBlock(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantWindow)
	in.Intents = Intents{Priority: "50", Hardware: "nvidia", Suspend: "true"}
	d := Plan(in)
	b := d.Block
	require.NotNil(t, b)
	assert.Equal(t, int32(50), b.Priority)
	assert.Equal(t, catalogv1alpha1.TranscodeHardware("nvidia"), b.Hardware)
	assert.True(t, b.Suspended)
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, b.Phase)
	assert.Equal(t, ReasonSuspended, b.Reason)
	assert.Empty(t, d.Events)

	resumed := inputs(b, probed("ph-1"), GrantNone)
	resumed.Intents = Intents{Priority: "50", Hardware: "nvidia"}
	r := Plan(resumed).Block
	assert.False(t, r.Suspended)
	assert.Equal(t, ReasonWaitingForSlot, r.Reason, "unsuspended, it waits for a slot again")

	in.Intents = Intents{Priority: "urgent", Hardware: "quantum"}
	bad := Plan(in)
	assert.Equal(t, []string{ReasonInvalidAnnotation, ReasonInvalidAnnotation}, reasons(bad.Events),
		"a bad standing value is ignored with a Warning")
	assert.Equal(t, in.Eligibility.Profile.Spec.Priority, bad.Block.Priority)
	assert.Equal(t, catalogv1alpha1.TranscodeHardware("auto"), bad.Block.Hardware, "the profile's hardware stands")
	assert.False(t, bad.Block.Suspended)

	verdict := &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSkipped, Reason: ReasonPlanSkip,
		Profile: "hevc", ProfileHash: in.Eligibility.Hash, ProbeHash: "ph-1"}
	v := inputs(verdict, probed("ph-1"), GrantNone)
	v.Intents = Intents{Priority: "50", Hardware: "nvidia", Suspend: "true"}
	assert.Equal(t, verdict, Plan(v).Block, "a verdict holds no window place to record them on")
}
