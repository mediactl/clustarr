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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// maxMessage is status.transcode.message's MaxLength.
const maxMessage = 1024

// Inputs is everything Plan reads; the adapter gathers it (§3.5: Plan is
// pure).
type Inputs struct {
	File         *catalogv1alpha1.MediaFile
	Prev         *catalogv1alpha1.TranscodeState  // v.Prev.Transcode
	Draft        *catalogv1alpha1.MediaFileStatus // v.Draft, after the probe planner
	ProbeCurrent bool
	Eligibility  Eligibility
	Ledger       Ledger
	Now          metav1.Time
}

// GrantOutcome is what Plan did with a held grant.
type GrantOutcome int

// The grant outcomes.
const (
	// GrantKeep: nothing granted, or nothing done with it.
	GrantKeep GrantOutcome = iota
	// GrantConfirm: the grant was used; confirm it once the apply lands.
	GrantConfirm
	// GrantRelease: the grant was not used; give it back.
	GrantRelease
)

// Decision is Plan's answer: the block to render (nil: none), what to do
// with a held grant, and when to come back.
type Decision struct {
	Block  *catalogv1alpha1.TranscodeState
	Grant  GrantOutcome
	Due    time.Time
	Again  bool
	Events []Event
}

// Event is one Kubernetes Event Plan asks for on the MediaFile.
type Event struct{ Type, Reason, Message string }

// Plan is the transcode planner's decision for one pass (§5.8-§5.11).
func Plan(in Inputs) Decision {
	prev := in.Prev
	switch {
	case prev != nil && prev.Phase == "":
		return Decision{Block: legacy(prev)}
	case prev != nil && prev.Phase == catalogv1alpha1.TranscodePhaseSwapping:
		return planSwap(in)
	case in.Ledger.Grant == GrantWindow:
		return planWindow(in)
	case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhasePending || prev.Phase == catalogv1alpha1.TranscodePhasePlanned):
		return planInWindow(in)
	case prev != nil && prev.Phase == catalogv1alpha1.TranscodePhaseSucceeded && prev.ProbeHash != "" &&
		prev.ProbeHash != in.Draft.ProbeHash:
		// staleTranscodeState, moved: the bytes the verdict described are gone
		// (a re-mux, a hand edit); the file stays Transcoded() by spec.original.
		b := prev.DeepCopy()
		b.Compliant, b.ProfileTag = false, ""
		return Decision{Block: b}
	}
	return Decision{Block: prev.DeepCopy()} // verdicts and Succeeded stay untouched (§5.11)
}

// legacy is §2.5 "Legacy objects": a phase-less block reads as Succeeded
// when it carries a profileTag, else as absent; jobRef and lastResult are
// not carried.
func legacy(p *catalogv1alpha1.TranscodeState) *catalogv1alpha1.TranscodeState {
	if p.ProfileTag == "" {
		return nil
	}
	return &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, Compliant: p.Compliant, ProfileTag: p.ProfileTag}
}

// SwapIncorporated reports whether the draft holds the probe of a Swapping
// block's output: probed no earlier than the worker finished (catalog's
// !finishedAt.After(probedAt), never the worker's strict After, §8.6 F6.3),
// and whether that probe's CLUSTARR_PROFILE is not the dispatched tag.
func SwapIncorporated(prev *catalogv1alpha1.TranscodeState, draft *catalogv1alpha1.MediaFileStatus) (done, mismatch bool) {
	if prev.FinishedAt == nil || draft.ProbedAt == nil || prev.FinishedAt.After(draft.ProbedAt.Time) {
		return false, false
	}
	tag := Tag(prev.Profile, prev.ProfileHash)
	return true, draft.MediaInfo == nil || draft.MediaInfo.TranscodeProfile != tag
}

// planSwap is §5.9 "The swap": Swapping waits for the probe of its output,
// then becomes Succeeded with the tag it was dispatched under, or Failed
// when the file at the output carries another.
func planSwap(in Inputs) Decision {
	b := in.Prev.DeepCopy()
	done, mismatch := SwapIncorporated(b, in.Draft)
	switch {
	case !done:
		return Decision{Block: b}
	case mismatch:
		b.Phase, b.Blocked, b.Reason = catalogv1alpha1.TranscodePhaseFailed, true, ReasonInvalidSource
		b.Message = truncate(fmt.Sprintf("the swapped file carries CLUSTARR_PROFILE %q, not %q; the takeover was refused",
			mediaTag(in.Draft), Tag(b.Profile, b.ProfileHash)), maxMessage)
		return Decision{Block: b, Events: []Event{{Type: "Warning", Reason: ReasonInvalidSource, Message: b.Message}}}
	}
	b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhaseSucceeded, "", "transcode incorporated"
	b.Compliant, b.ProfileTag, b.Result, b.ProbeHash = true, Tag(b.Profile, b.ProfileHash), nil, in.Draft.ProbeHash
	return Decision{Block: b, Events: []Event{{Type: "Normal", Reason: "Succeeded", Message: "incorporated " + b.ProfileTag}}}
}

// planWindow is §5.8 "Window grant": plan from the stored probe for the
// profile's hardware; a verdict, Planned, or Pending while the probe is
// pending or a standalone graft holds the file; release when ineligible.
func planWindow(in Inputs) Decision {
	e := in.Eligibility
	if verdictHolds(in.Prev, e, in.Draft) {
		return Decision{Block: in.Prev.DeepCopy(), Grant: GrantRelease}
	}
	if !e.Eligible && e.Pending == "" {
		return Decision{Block: keepFact(in), Grant: GrantRelease}
	}
	b := &catalogv1alpha1.TranscodeState{
		Profile: e.Profile.Name, ProfileHash: e.Hash, ProfileTag: e.Tag,
		ProbeHash: in.Draft.ProbeHash, Hardware: catalogv1alpha1.TranscodeHardware(hardwareOf(e.Profile)),
		Priority: e.Priority,
	}
	carryDispatch(in.Prev, b)
	if e.Pending != "" {
		b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhasePending, e.Pending, e.Why
		return Decision{Block: b, Grant: GrantConfirm}
	}
	p, fail := PlanFor(in.File.Spec.Path, e.Profile, e.Hash, in.File, in.Draft, nil, in.Ledger.Device)
	switch {
	case fail != nil:
		b.Phase, b.Reason, b.Message, b.Blocked = catalogv1alpha1.TranscodePhaseFailed, fail.Reason, truncate(fail.Message, maxMessage), true
	case p.Skips():
		b.Phase, b.Reason = catalogv1alpha1.TranscodePhaseSkipped, ReasonPlanSkip
		b.Message = truncate("skipped: "+firstNonEmpty(p.Reject, p.Plan.Reason), maxMessage)
	default:
		b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhasePlanned, ReasonWaitingForSlot, "waiting for a free slot"
		b.PlannedAt, b.Plan = new(in.Now), StatusPlan(&p.Plan)
	}
	return Decision{Block: b, Grant: GrantConfirm}
}

// verdictHolds reports a Skipped or Failed block that stops planning: only
// for its own (profileHash, probeHash) (§5.11, D13).
func verdictHolds(p *catalogv1alpha1.TranscodeState, e Eligibility, st *catalogv1alpha1.MediaFileStatus) bool {
	return p != nil && (p.Phase == catalogv1alpha1.TranscodePhaseSkipped || p.Phase == catalogv1alpha1.TranscodePhaseFailed) &&
		p.ProfileHash == e.Hash && p.ProbeHash == st.ProbeHash
}

// planInWindow keeps a Pending or Planned file's place while it is
// eligible; a Pending one is planned once what held it clears; one that
// stopped being eligible is cleared (§5.11 "Leaving the window"). A profile
// edit does not evict a Planned file: dispatch re-plans it.
func planInWindow(in Inputs) Decision {
	e := in.Eligibility
	switch {
	case !e.Eligible && e.Pending == "":
		return Decision{Block: keepFact(in)}
	case in.Prev.Phase == catalogv1alpha1.TranscodePhasePending && e.Eligible:
		d := planWindow(in)
		d.Grant = GrantKeep // it already holds its place
		return d
	case in.Prev.Phase == catalogv1alpha1.TranscodePhasePending:
		b := in.Prev.DeepCopy()
		b.Reason, b.Message = e.Pending, e.Why
		return Decision{Block: b}
	}
	b := in.Prev.DeepCopy()
	if in.Ledger.WaitReason != "" && in.Ledger.WaitReason != b.Message {
		b.Reason, b.Message = in.Ledger.WaitCause, truncate(in.Ledger.WaitReason, maxMessage) // written only on change (§5.11)
	}
	return Decision{Block: b}
}

// keepFact is what clearing a block leaves: nothing, unless a withdrawn
// dispatch's fact can still land (F6.5 fills the condition; until then nil).
func keepFact(Inputs) *catalogv1alpha1.TranscodeState { return nil }

// carryDispatch keeps the last dispatch on a re-planned block, so a fact
// for it is still matched and lastSeq stays the floor (§2.3).
func carryDispatch(prev, b *catalogv1alpha1.TranscodeState) {
	if prev != nil && prev.Dispatch != nil {
		b.Dispatch = prev.Dispatch.DeepCopy()
	}
}

// hardwareOf is tp's spec.hardware, empty read as the CRD default, auto.
func hardwareOf(tp *transcodev1alpha1.TranscodeProfile) transcodev1alpha1.Hardware {
	if tp.Spec.Hardware == "" {
		return transcodev1alpha1.HardwareAuto
	}
	return tp.Spec.Hardware
}

// mediaTag is the draft probe's CLUSTARR_PROFILE, "" when none.
func mediaTag(st *catalogv1alpha1.MediaFileStatus) string {
	if st.MediaInfo == nil {
		return ""
	}
	return st.MediaInfo.TranscodeProfile
}
