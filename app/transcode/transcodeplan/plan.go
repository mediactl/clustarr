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
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/task"
)

// maxMessage is status.transcode.message's MaxLength.
const maxMessage = 1024

// Inputs is everything Plan reads; the adapter gathers it (§3.5: Plan is
// pure).
type Inputs struct {
	File *catalogv1alpha1.MediaFile
	// OriginalLanguage is the file's movie's or series' original language
	// (itemread.OriginalLanguage), "" when unknown: the standard's
	// "original" in a profile's language lists, carried to the worker in
	// the task.
	OriginalLanguage string
	Prev             *catalogv1alpha1.TranscodeState  // v.Prev.Transcode
	Draft            *catalogv1alpha1.MediaFileStatus // v.Draft, after the probe planner
	ProbeCurrent     bool
	Eligibility      Eligibility
	Ledger           Ledger
	Now              metav1.Time

	// Record is the file's clustarr-transcodes record, read while the block
	// is in flight, a dispatch grant is held, or a withdrawn dispatch's fact
	// can still land (§4.10); RecordRev its revision (0: absent).
	Record    *task.Record
	RecordRev uint64
	// Folders are the namespace's RootFolders, read when a task is built.
	Folders []catalogv1alpha1.RootFolder
	// Issue is the pass's sequence issuer (remediation.View.Issue).
	Issue func(recordSeq int64) int64
	// Lease is what Gather read of the file's lease while it is Running.
	Lease LeaseView
	// TaskStored is whether the stream still holds the file's task, read
	// only once a lease has been missing for leaseLostGrace; nil: unread.
	TaskStored *bool
	// Profiles are the TranscodeProfiles by name.
	Profiles map[string]*transcodev1alpha1.TranscodeProfile

	// Intents are the file's transcode annotations (ParseIntents), Nonces
	// the one-shot nonces already handled (status.handledNonces).
	Intents Intents
	Nonces  catalogv1alpha1.HandledNonces
	// DeadLettered is the DLQ projector's annotation on the file
	// (k8s.AnnotationDeadLettered): "<subject>@<RFC3339>".
	DeadLettered string
	// SpecPathMissing, OutputExists and OutputSize are the stats of
	// spec.path and the last dispatch's output, read while a block has a
	// dispatch (FileMissing, §5.9).
	SpecPathMissing bool
	OutputExists    bool
	OutputSize      int64

	// PrevGraft is the file's stored graft block (v.Prev.Graft): a dispatch
	// is held while it has not recorded the joined graft of the block's
	// last dispatch (graftplan.JoinPending, §5.13).
	PrevGraft *catalogv1alpha1.GraftState
	// Joinable is the graft a dispatch carries (graftplan.Join), gathered on
	// a dispatch grant and for an owed republish of a block that carries a
	// join; nil carries none.
	Joinable *graftplan.Joinable
}

// profile is the TranscodeProfile named name: from Profiles, else the
// winning profile when it is that one.
func (in Inputs) profile(name string) *transcodev1alpha1.TranscodeProfile {
	if tp, ok := in.Profiles[name]; ok {
		return tp
	}
	if p := in.Eligibility.Profile; p != nil && p.Name == name && in.Profiles == nil {
		return p
	}
	return nil
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
// with a held grant, and when to come back -- and what the apply owes after
// it lands, in §3.8's order: the record request, the task publish, the
// ledger confirmation, the history and the R9 observation.
type Decision struct {
	Block  *catalogv1alpha1.TranscodeState
	Grant  GrantOutcome
	Due    time.Time
	Again  bool
	Events []Event

	// ConfirmSeq is the dispatch a confirmed dispatch grant became.
	ConfirmSeq int64
	// Request is the record to write at RequestRev (0: create).
	Request    *task.Record
	RequestRev uint64
	// Publish is the task to publish under its Msg-Id; Republish when the
	// record already says requested at its Seq.
	Publish   *task.Task
	Republish bool
	// History are the events the transition owes.
	History []HistoryEvent
	// Observe is the R9 observation of a dispatch that just became
	// terminal after running.
	Observe *Terminal
	// Unpaced exempts the apply from the status-write limiter (§3.7): a
	// dispatch, an incorporation.
	Unpaced bool
	// Records are the record states this pass incorporated.
	Records []string
	// Withdraw is the dispatch seq a withdrawal is owed for (§5.10); 0 is
	// none.
	Withdraw int64
	// Nonces are the one-shot nonces to record as handled; nil keeps them.
	Nonces *catalogv1alpha1.HandledNonces
	// Dispatched is clustarr_transcode_dispatches_total's outcome this pass
	// owes ("unclaimed", "lost", "withdrawn"), "" for none.
	Dispatched string
}

// Event is one Kubernetes Event Plan asks for on the MediaFile.
type Event struct{ Type, Reason, Message string }

// Plan is the transcode planner's decision for one pass (§5.8-§5.11): the
// intents first (§2.9), then the block's row, then the standing intents in
// force on a window block.
func Plan(in Inputs) Decision {
	nonces, evs := applyIntents(&in)
	d := plan(in)
	d.Nonces, d.Events = nonces, append(evs, d.Events...)
	applyStanding(in, &d)
	return d
}

func plan(in Inputs) Decision {
	prev := in.Prev
	switch {
	case prev != nil && prev.Phase == "":
		return Decision{Block: legacy(prev)}
	case factAfterWithdrawal(in):
		return incorporate(in, prev.DeepCopy(), in.Record)
	case fileMissingSwap(in):
		return recoverSwap(in)
	case prev != nil && prev.Phase == catalogv1alpha1.TranscodePhaseSwapping:
		return planSwap(in)
	case in.Ledger.Grant == GrantDispatch:
		return planDispatch(in)
	case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhaseQueued || prev.Phase == catalogv1alpha1.TranscodePhaseRunning):
		return planInFlight(in)
	case in.Ledger.Grant == GrantWindow:
		return planWindow(in)
	case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhasePending || prev.Phase == catalogv1alpha1.TranscodePhasePlanned):
		return planInWindow(in)
	case prev != nil && prev.Phase == catalogv1alpha1.TranscodePhaseSucceeded && prev.ProbeHash != "" &&
		prev.ProbeHash != in.Draft.ProbeHash:
		b := prev.DeepCopy()
		if tag := succeededTag(prev); tag != "" && mediaTag(in.Draft) == tag {
			// Moved, not changed: the probe hash hashes the path, so a rename
			// (the naming planner's, renameTranscoded's) moves it, but the file
			// still carries this transcode's CLUSTARR_PROFILE. The verdict
			// follows the file, and a tag an earlier pass dropped is restored
			// (found by the smoke test, 2026-10-07).
			b.ProbeHash, b.ProfileTag, b.Compliant = in.Draft.ProbeHash, tag, true
			return Decision{Block: b}
		}
		// staleTranscodeState, moved: the bytes the verdict described are gone
		// (a re-mux, a hand edit); the file stays Transcoded() by spec.original.
		b.Compliant, b.ProfileTag = false, ""
		return Decision{Block: b}
	case prev != nil && prev.Reason == ReasonProfileGone && prev.Dispatch != nil && prev.Dispatch.Withdrawn:
		// Kept only while a late fact may land, then cleared (§5.9).
		return Decision{Block: keepFact(in)}
	}
	return Decision{Block: prev.DeepCopy()} // verdicts and Succeeded stay untouched (§5.11)
}

// succeededTag is the CLUSTARR_PROFILE a Succeeded block's swap stamped:
// its profileTag, or, once the stale rule dropped it, the profile and hash
// it was dispatched under.
func succeededTag(b *catalogv1alpha1.TranscodeState) string {
	if b.ProfileTag != "" {
		return b.ProfileTag
	}
	if b.Profile == "" || b.ProfileHash == "" {
		return ""
	}
	return Tag(b.Profile, b.ProfileHash)
}

// legacy is §2.5 "Legacy objects": a phase-less block a 0.4.x install
// stored reads as Succeeded when it carries a profileTag, else as absent;
// its other 0.4.x leaves are gone from the schema and pruned on read.
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
	p, fail := PlanFor(in.File.Spec.Path, e.Profile, e.Hash, in.File, in.Draft, nil, in.Ledger.Device, in.OriginalLanguage)
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
