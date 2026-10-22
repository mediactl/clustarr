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
	"strings"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/records"
)

// withdrawalFailedPrefix starts the message a pass writes when the
// withdrawal it owes failed (§5.10): the phase stays, and the next pass
// withdraws again.
const withdrawalFailedPrefix = "withdrawal failed: "

// withdrawingMessage is pass 1's message for a withdrawal of seq.
func withdrawingMessage(seq int64) string { return fmt.Sprintf("withdrawing attempt %d", seq) }

// withdrawing reports whether b is taking back its dispatch: pass 1 of
// §5.10 set the cause with the message "withdrawing attempt S", or a failed
// withdrawal wrote why.
func withdrawing(b *catalogv1alpha1.TranscodeState) bool {
	return b != nil && b.Dispatch.InFlight() &&
		(b.Message == withdrawingMessage(b.Dispatch.Seq) || strings.HasPrefix(b.Message, withdrawalFailedPrefix))
}

// startWithdrawal is pass 1: keep the phase, set the cause, emit Withdraw.
// Writing the cause's state first and crashing would leave a task the
// rebuilt ledger cannot see.
func startWithdrawal(b *catalogv1alpha1.TranscodeState, cause string) Decision {
	b.Reason, b.Message = cause, withdrawingMessage(b.Dispatch.Seq)
	return Decision{
		Block: b, Withdraw: b.Dispatch.Seq, Unpaced: true,
		Events: []Event{{Type: "Normal", Reason: "Withdrawn", Message: fmt.Sprintf("withdrawing attempt %d: %s", b.Dispatch.Seq, cause)}},
	}
}

// rewithdraw re-emits the withdrawal of a block that is still taking its
// dispatch back -- never the owed request (§5.10) -- writing the last
// failure when admission holds one.
func rewithdraw(in Inputs, b *catalogv1alpha1.TranscodeState) Decision {
	if msg := in.Ledger.WithdrawFailed; msg != "" {
		b.Message = truncate(withdrawalFailedPrefix+msg, maxMessage)
	}
	return Decision{Block: b, Withdraw: b.Dispatch.Seq, Due: in.Now.Add(inflightRecheck)}
}

// finishWithdrawal is pass 2: the record is withdrawn at S (answered or
// failed there is incorporated first); write what the cause dictates, close
// the dispatch and mark it withdrawn, so a fact that lands afterwards is
// still incorporated within the fact window (§4.10).
func finishWithdrawal(in Inputs, b *catalogv1alpha1.TranscodeState) Decision {
	seq := b.Dispatch.Seq
	b.Dispatch.AnsweredSeq, b.Dispatch.Withdrawn = seq, true
	b.WorkerPod, b.StartedAt = "", nil
	switch b.Reason {
	case ReasonCancelled:
		b.Phase, b.Message = catalogv1alpha1.TranscodePhaseSkipped, fmt.Sprintf("attempt %d cancelled", seq)
	case ReasonProfileGone:
		b.Phase, b.Message = catalogv1alpha1.TranscodePhaseSkipped, "its TranscodeProfile was deleted"
	case ReasonExcluded:
		b.Phase, b.Message = catalogv1alpha1.TranscodePhaseSkipped, fmt.Sprintf("attempt %d withdrawn: excluded by %s", seq, catalogv1alpha1.AnnotationTranscodeExclude)
	case ReasonDeadLettered:
		b.Phase, b.Blocked = catalogv1alpha1.TranscodePhaseFailed, true
		b.Message = fmt.Sprintf("attempt %d was dead-lettered after its last delivery", seq)
	case ReasonDeadlineExceeded:
		b.Phase, b.Blocked = catalogv1alpha1.TranscodePhaseFailed, false
		b.Message = fmt.Sprintf("attempt %d ran past its deadline", seq)
	case ReasonSourceChanged:
		b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhasePending, ReasonProbePending, "a probe of new bytes is pending"
	case ReasonUnclaimed:
		b.Phase, b.Message = catalogv1alpha1.TranscodePhasePlanned, fmt.Sprintf("attempt %d was not claimed within 50m; requeued", seq)
	case ReasonRerouted:
		b.Phase = catalogv1alpha1.TranscodePhasePlanned
		if dir := in.Ledger.Directive; dir != nil && !dir.KeepGPU {
			b.FallbackReason = truncate(dir.Reason, maxFallbackReason)
		}
		b.Message = fmt.Sprintf("attempt %d withdrawn: rerouted", seq)
	default: // Suspended, Orphaned
		b.Phase, b.Message = catalogv1alpha1.TranscodePhasePlanned, fmt.Sprintf("attempt %d withdrawn: %s", seq, b.Reason)
	}
	if b.Phase == catalogv1alpha1.TranscodePhaseFailed || b.Phase == catalogv1alpha1.TranscodePhaseSkipped {
		b.FinishedAt = &in.Now
	}
	return Decision{
		Block: b, Unpaced: true, History: []HistoryEvent{{Action: ActionWithdrawn, Seq: seq, Reason: b.Reason}},
		Events: []Event{{Type: "Normal", Reason: "Withdrawn", Message: b.Message}},
	}
}

// ActionWithdrawn is the history action of a closed withdrawal.
const ActionWithdrawn = "withdrawn"

// withdrawCause is the first of §5.9's rows that take a Queued or Running
// dispatch back, in order, "" when none holds: a pending cancel, suspend,
// the task dead-lettered, the profile gone, the pool not the profile's
// (Orphaned), a reroute directive at the seq, new bytes under a Queued
// dispatch, a Queued dispatch unclaimed past the republish window, a claimed
// one past its deadline.
func withdrawCause(in Inputs, b *catalogv1alpha1.TranscodeState) (string, []Event) {
	seq := b.Dispatch.Seq
	if v := in.Intents.Cancel; v != "" && catalogv1alpha1.HandledNonce(v) != in.Nonces.TranscodeCancel && catalogv1alpha1.ValidNonce(v) {
		return ReasonCancelled, nil
	}
	if in.Intents.Suspend == "true" {
		return ReasonSuspended, nil
	}
	if in.Eligibility.Excluded {
		return ReasonExcluded, nil
	}
	if deadLettered(in, b) {
		return ReasonDeadLettered, []Event{{Type: "Warning", Reason: ReasonDeadLettered, Message: fmt.Sprintf("the task of attempt %d was dead-lettered", seq)}}
	}
	tp := in.profile(b.Profile)
	if tp == nil {
		return ReasonProfileGone, nil
	}
	if b.Pool != "" && b.Pool != pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: hardwareClass(b)}) {
		return ReasonOrphaned, nil
	}
	if d := in.Ledger.Directive; d != nil && d.Kind == DirectiveReroute && d.Seq == seq {
		return ReasonRerouted, nil
	}
	rec := in.Record
	queued := b.Phase == catalogv1alpha1.TranscodePhaseQueued
	if queued && in.Draft.ProbeHash != b.ProbeHash {
		return ReasonSourceChanged, nil
	}
	if queued && rec != nil && rec.Seq == seq && rec.State == records.StateRequested &&
		!in.Now.Time.Before(b.Dispatch.DispatchedAt.Add(records.RepublishWindow)) {
		return ReasonUnclaimed, []Event{{
			Type: "Warning", Reason: "DispatchUnclaimed",
			Message: fmt.Sprintf("attempt %d was not claimed within %s", seq, records.RepublishWindow),
		}}
	}
	if b.Phase == catalogv1alpha1.TranscodePhaseRunning && rec != nil && rec.Seq == seq && rec.State == records.StateClaimed {
		claimed := b.Dispatch.DispatchedAt.Time
		if rec.ClaimedAt != nil {
			claimed = *rec.ClaimedAt
		}
		if in.Now.After(claimed.Add(jobspec.ActiveDeadline(tp.Spec) + deadlineGrace)) {
			return ReasonDeadlineExceeded, nil
		}
	}
	return "", nil
}

// deadLettered reports the DLQ projector's annotation naming this file's
// task, dated after the dispatch (§5.9).
func deadLettered(in Inputs, b *catalogv1alpha1.TranscodeState) bool {
	v := in.DeadLettered
	if v == "" {
		return false
	}
	subject, at, _ := strings.Cut(v, "@")
	want := events.WorkTranscodeTaskSubject("x", "x", string(in.File.UID))
	last := want[strings.LastIndex(want, ".")+1:]
	if !strings.HasPrefix(subject, "clustarr.work.transcode.task.") || !strings.HasSuffix(subject, "."+last) {
		return false
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil && t.Before(b.Dispatch.DispatchedAt.Time) {
		return false // an earlier dispatch's dead letter
	}
	return true
}

// hardwareClass is the block's class as a pool class.
func hardwareClass(b *catalogv1alpha1.TranscodeState) transcodev1alpha1.Hardware {
	return transcodev1alpha1.Hardware(b.Class)
}

// withdrawnAt reports a record withdrawn at seq: the withdrawal of seq
// landed.
func withdrawnAt(rec *task.Record, seq int64) bool {
	return rec != nil && rec.Seq == seq && rec.State == records.StateWithdrawn
}
