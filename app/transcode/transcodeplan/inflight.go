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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/records"
)

// planInFlight is §5.9's rows for a Queued or Running block that need no
// withdrawal, in order: an unincorporated fact at an earlier seq closes the
// current one and is incorporated; a record absent or below the dispatch
// owes the request and the publish; requested at the seq republishes inside
// records.RepublishWindow; claimed is Running; a Running dispatch whose
// lease is gone past leaseLostGrace and whose task is no longer stored is
// WorkerLost; answered is incorporated (Decide); failed is retriable with
// its cause.
func planInFlight(in Inputs) Decision {
	b := in.Prev.DeepCopy()
	if b.Dispatch == nil {
		return Decision{Block: b, Due: in.Now.Add(inflightRecheck)}
	}
	seq, rec := b.Dispatch.Seq, in.Record
	queued := b.Phase == catalogv1alpha1.TranscodePhaseQueued
	switch {
	case rec != nil && rec.Seq == seq && (rec.State == records.StateAnswered || rec.State == records.StateFailed):
		// An answer at the seq is incorporated, even over a withdrawal
		// (§5.10: withdrawn writes nothing over it).
		return incorporate(in, b, rec)
	case withdrawing(b) && withdrawnAt(rec, seq):
		d := finishWithdrawal(in, b)
		d.Dispatched = "withdrawn"
		return d
	case withdrawing(b):
		return rewithdraw(in, b)
	}
	if cause, evs := withdrawCause(in, b); cause != "" {
		d := startWithdrawal(b, cause)
		d.Events = append(d.Events, evs...)
		if cause == ReasonUnclaimed {
			d.Dispatched = "unclaimed"
		}
		return d
	}
	switch {
	case queued && rec != nil && task.RecordSpec.Fact(rec) && rec.Seq < seq && rec.Seq > b.Dispatch.AnsweredSeq:
		// The request for seq missed its CAS over this fact, so nothing was
		// published at seq: close it without a record write, then
		// incorporate the fact (§4.10).
		d := incorporate(in, b, rec)
		d.Block.Dispatch.AnsweredSeq = seq
		return d
	case rec == nil || rec.Seq < seq:
		if queued {
			return owe(in, b, true)
		}
		// Running with its record lost (a lost bucket): the lease path.
		return running(in, b, Decision{})
	case rec.Seq > seq:
		// A newer request owns the key: nothing the block dispatched.
		return Decision{Block: b, Due: in.Now.Add(inflightRecheck)}
	}
	switch rec.State {
	case records.StateAnswered, records.StateFailed:
		return incorporate(in, b, rec)
	case records.StateRequested:
		if in.Now.Sub(b.Dispatch.DispatchedAt.Time) < records.RepublishWindow {
			d := owe(in, b, false)
			if msg := in.Ledger.PublishFailed; msg != "" {
				d.Block.Message = truncate(fmt.Sprintf("attempt %d: publish failed: %s; retrying", seq, msg), maxMessage)
			}
			return d
		}
		return Decision{Block: b, Due: in.Now.Add(inflightRecheck)} // withdrawCause took it, Unclaimed
	case records.StateClaimed:
		var d Decision
		if b.Phase != catalogv1alpha1.TranscodePhaseRunning {
			b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhaseRunning, "", fmt.Sprintf("attempt %d running", seq)
			b.WorkerPod = truncate(rec.Writer, 253)
			if rec.ClaimedAt != nil {
				b.StartedAt = &metav1.Time{Time: *rec.ClaimedAt}
			}
			d.History = []HistoryEvent{{Action: events.ActionStarted, Seq: seq}}
			d.Unpaced = true
		}
		return running(in, b, d)
	}
	return Decision{Block: b, Due: in.Now.Add(inflightRecheck)}
}

// running is the lease row of §5.9 for a Running block: a worker whose
// lease has been gone past leaseLostGrace, with no task left in the stream
// to redeliver, is lost -- Decided as retriable, reason WorkerLost, counted
// toward MaxAttempts.
func running(in Inputs, b *catalogv1alpha1.TranscodeState, d Decision) Decision {
	seq := b.Dispatch.Seq
	if b.Phase == catalogv1alpha1.TranscodePhaseRunning && lost(in) {
		a := task.Answer{
			Outcome: task.OutcomeFailed, Reason: ReasonWorkerLost,
			Message: fmt.Sprintf("the worker of attempt %d is gone: its lease lapsed and its task is no longer stored", seq),
		}
		n := retry(ReasonWorkerLost, a.Message, *b)
		applyAnswer(b, a, n, in.Now.Time)
		b.Dispatch.AnsweredSeq = seq
		d.History = append(d.History, HistoryEvent{Action: events.ActionFailed, Seq: seq, Reason: ReasonWorkerLost})
		d.Events = append(d.Events, Event{Type: "Warning", Reason: ReasonWorkerLost, Message: a.Message})
		d.Unpaced, d.Records, d.Dispatched = true, []string{"lost"}, "lost"
	}
	d.Block, d.Due = b, in.Now.Add(inflightRecheck)
	return d
}

// lost reports §5.9's lost worker: a Running dispatch whose lease has been
// gone for leaseLostGrace and whose task the stream no longer holds (a
// stored one is redelivered by JetStream after its AckWait).
func lost(in Inputs) bool {
	return in.Lease.Missing && !in.Lease.MissingSince.IsZero() && in.Now.Sub(in.Lease.MissingSince) > leaseLostGrace &&
		in.TaskStored != nil && !*in.TaskStored
}

// owe is the owed effects of b's dispatch (§4.7): the request when write
// is set (the record is absent or older), and the publish under the same
// Msg-Id, every republishEvery.
func owe(in Inputs, b *catalogv1alpha1.TranscodeState, write bool) Decision {
	d := Decision{Block: b, Due: in.Now.Add(republishEvery)}
	t, err := taskFor(in, b)
	if err != nil {
		// Its profile is gone or its RootFolder unreadable: F6.5's orphan
		// and withdrawal rows take it.
		d.Due = in.Now.Add(inflightRecheck)
		return d
	}
	if write {
		d.Request, d.RequestRev = requested(in.File, b.Dispatch.Seq, t.Inputs, b.Dispatch.DispatchedAt.Time), in.RecordRev
	}
	d.Publish, d.Republish = t, !write
	return d
}

// incorporate is the answered and failed rows of §5.9: Decide over the
// answer (a failed record -- a final delivery's -- is retriable with its
// cause, never WorkerLost), the dispatch closed, timestamps from the record,
// the R9 observation on a terminal edge and the history.
func incorporate(in Inputs, b *catalogv1alpha1.TranscodeState, rec *task.Record) Decision {
	seq := rec.Seq
	var a task.Answer
	var n Next
	switch {
	case rec.State == records.StateFailed || rec.Answer == nil:
		a = task.Answer{Outcome: task.OutcomeFailed, Reason: task.ReasonRetriable, Message: rec.Failure, FinishedAt: rec.AnsweredAt}
		if a.Message == "" {
			a.Message = "the dispatch failed on its last delivery"
		}
		n = retry(task.ReasonRetriable, a.Message, *b)
	default:
		a = *rec.Answer
		if a.FinishedAt == nil {
			a.FinishedAt = rec.AnsweredAt
		}
		n = Decide(a, *b, choosesClass(b, in.Eligibility), kept(in, b, a))
	}
	applyAnswer(b, a, n, in.Now.Time)
	if b.Dispatch != nil {
		b.Dispatch.AnsweredSeq, b.Dispatch.Withdrawn = max(b.Dispatch.AnsweredSeq, seq), false
	}
	d := Decision{Block: b, Unpaced: true, Records: []string{rec.State}}
	action := events.ActionFailed
	switch b.Phase {
	case catalogv1alpha1.TranscodePhaseSwapping, catalogv1alpha1.TranscodePhaseSucceeded:
		action = events.ActionSucceeded
		d.Again = b.Phase == catalogv1alpha1.TranscodePhaseSwapping // the next pass requests the swap's probe
	case catalogv1alpha1.TranscodePhaseSkipped:
		action = events.ActionSkipped
	}
	if !n.NoOp {
		d.History = []HistoryEvent{{Action: action, Seq: seq, Reason: b.Reason}}
	}
	if b.Phase != catalogv1alpha1.TranscodePhasePlanned && !n.NoOp {
		d.Observe = terminalOf(b, in.Draft)
	}
	if b.Phase == catalogv1alpha1.TranscodePhaseFailed {
		d.Events = append(d.Events, Event{Type: "Warning", Reason: firstNonEmpty(b.Reason, "Failed"), Message: b.Message})
	}
	return d
}

// kept reports a success that is a copy beside a kept source: the winning
// profile keeps the source (policy.replaceSource=false) and the output has
// a name of its own. It never swaps (§5.14).
func kept(in Inputs, b *catalogv1alpha1.TranscodeState, a task.Answer) bool {
	tp := in.profile(b.Profile)
	if tp == nil || a.Result == nil || a.Result.OutputPath == "" || a.Result.OutputPath == in.File.Spec.Path {
		return false
	}
	return !jobspec.ReplaceSource(tp.Spec.Policy)
}
