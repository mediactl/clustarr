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
	"errors"
	"fmt"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// The dispatch timings (loop spec §5.3).
const (
	// republishEvery is how often a requested, unclaimed dispatch is
	// published again under its Msg-Id while records.RepublishWindow lasts.
	republishEvery = time.Minute
	// leaseLostGrace is how long a Running dispatch's lease may be gone
	// before the stream is asked whether its task is still stored.
	leaseLostGrace = LeaseLostGrace
	// inflightRecheck is how often an in-flight block is looked at with
	// nothing else due.
	inflightRecheck = time.Minute
)

// LeaseLostGrace is how long a Running dispatch's lease may be gone before
// the stream is asked whether its task is still stored (§5.3): twice the
// lease's TTL.
const LeaseLostGrace = 2 * events.TranscodeLeaseTTL

// LeaseView is what the adapter read of the file's lease.
type LeaseView struct {
	Missing      bool
	MissingSince time.Time // the ledger's note (leaseMissingSince)
}

// errNoProfile is a block whose TranscodeProfile is gone: its task cannot
// be rebuilt (the withdrawal rows of F6.5 take it).
var errNoProfile = errors.New("its TranscodeProfile is gone")

// planDispatch is §5.8 for a held dispatch grant of class C, only from a
// Planned view: the block is Queued first; the record and the publish are
// owed effects after the apply, with the same seq in the Msg-Id.
func planDispatch(in Inputs) Decision {
	prev := in.Prev
	if prev == nil || prev.Phase != catalogv1alpha1.TranscodePhasePlanned {
		rest := in
		rest.Ledger = Ledger{WaitReason: in.Ledger.WaitReason, WaitCause: in.Ledger.WaitCause}
		d := Plan(rest)
		d.Grant = GrantRelease
		return d
	}
	if unincorporatedFact(in.Record, prev) {
		d := incorporate(in, prev.DeepCopy(), in.Record) // §4.10: never dispatch over a fact
		d.Grant = GrantRelease
		return d
	}
	if s, pending := graftplan.JoinPending(prev, in.PrevGraft); pending {
		// The graft that rode along with attempt s has not recorded its
		// result: a new request would replace the record it reads (§5.13).
		b := prev.DeepCopy()
		b.Message = truncate(fmt.Sprintf("waiting for the joined graft of attempt %d to be recorded", s), maxMessage)
		return Decision{Block: b, Grant: GrantRelease, Due: in.Now.Add(inflightRecheck)}
	}
	e := in.Eligibility
	b := prev.DeepCopy()
	if !e.Eligible || e.Profile == nil {
		d := Plan(Inputs{
			File: in.File, Prev: prev, Draft: in.Draft, ProbeCurrent: in.ProbeCurrent, Eligibility: e, Now: in.Now,
			Record: in.Record, RecordRev: in.RecordRev, Folders: in.Folders, Issue: in.Issue, Profiles: in.Profiles,
		})
		d.Grant = GrantRelease
		return d
	}
	class := in.Ledger.Class
	p, fail := PlanFor(in.File.Spec.Path, e.Profile, e.Hash, in.File, in.Draft, &class, in.Ledger.Device, in.OriginalLanguage)
	switch {
	case fail != nil:
		b.Phase, b.Blocked, b.Reason, b.Message = catalogv1alpha1.TranscodePhaseFailed, true, fail.Reason, truncate(fail.Message, maxMessage)
		return Decision{Block: b, Grant: GrantRelease}
	case p.Skips():
		b.Phase, b.Reason, b.Plan = catalogv1alpha1.TranscodePhaseSkipped, ReasonPlanSkip, nil
		b.Message = truncate("skipped: "+firstNonEmpty(p.Reject, p.Plan.Reason), maxMessage)
		return Decision{Block: b, Grant: GrantRelease}
	case !runsIn(&p, class):
		// The plan for C needs another class: stay Planned; an auto file
		// records why, so its next dispatch goes to cpu (keepPlanned).
		if choosesClass(b, e) {
			b.FallbackReason = truncate(fmt.Sprintf("a plan for %s encodes with %s, which needs no GPU", class, p.Plan.Video.Encoder), maxFallbackReason)
		}
		b.Plan = StatusPlan(&p.Plan)
		return Decision{Block: b, Grant: GrantRelease}
	}
	seq := issue(in)
	plan := StatusPlan(&p.Plan)
	mf := in.File.DeepCopy()
	mf.Status = *in.Draft
	t, err := jobspec.BuildTask(mf, e.Profile, e.Hash, in.Folders, seq, class, plan)
	if err != nil {
		b.Phase, b.Blocked, b.Reason, b.Message = catalogv1alpha1.TranscodePhaseFailed, true, ReasonInvalidSource, truncate("cannot dispatch: "+err.Error(), maxMessage)
		return Decision{Block: b, Grant: GrantRelease}
	}
	k := pool.Key{Profile: e.Profile.Name, ProfileUID: e.Profile.UID, Class: class}
	b.Phase, b.Reason, b.Blocked = catalogv1alpha1.TranscodePhaseQueued, "", false
	b.Message = fmt.Sprintf("attempt %d dispatched to %s: %s", seq, pool.Name(k), plan.Describe())
	b.Dispatch = &catalogv1alpha1.Dispatch{Seq: seq, DispatchedAt: in.Now}
	b.Attempts++
	b.Class, b.Pool, b.Plan = catalogv1alpha1.TranscodeClass(class), pool.Name(k), plan
	b.Profile, b.ProfileHash, b.ProfileTag, b.ProbeHash = e.Profile.Name, e.Hash, e.Tag, in.Draft.ProbeHash
	b.WorkerPod, b.StartedAt, b.FinishedAt, b.NextAttemptAt, b.StderrTail, b.JoinedGraft, b.Result = "", nil, nil, nil, "", nil, nil
	if j := in.Joinable; j != nil {
		// The file's graft rides along: the worker grafts in the same pass,
		// and the block records the join the graft planner derives its
		// Running block from (§5.13, D42).
		g := j.Task
		t.Graft, b.JoinedGraft = &g, j.Join.DeepCopy()
	}
	return Decision{
		Block: b, Grant: GrantConfirm, ConfirmSeq: seq,
		Request: requested(in.File, seq, t.Inputs, in.Now.Time), RequestRev: in.RecordRev, Publish: &t,
		History: []HistoryEvent{{Action: events.ActionQueued, Seq: seq}},
		Due:     in.Now.Add(republishEvery), Unpaced: true,
		Events: []Event{{Type: "Normal", Reason: "Dispatched", Message: b.Message}},
	}
}

// issue is the dispatch's sequence: the view's issuer, which raises the
// pass's floor, or NextSeq over the record alone.
func issue(in Inputs) int64 {
	if in.Issue != nil {
		return in.Issue(recordSeq(in.Record))
	}
	return records.NextSeq(recordSeq(in.Record), 0, in.Now.Time)
}

// recordSeq is rec's Seq, 0 when absent.
func recordSeq(rec *task.Record) int64 {
	if rec == nil {
		return 0
	}
	return rec.Seq
}

// requested is the record a dispatch at seq writes.
func requested(mf *catalogv1alpha1.MediaFile, seq int64, inputs task.Inputs, now time.Time) *task.Record {
	return &task.Record{
		RecordHeader: schema.RecordHeader{
			Schema: task.RecordSchema, MediaFile: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)},
			Seq: seq, State: records.StateRequested, RequestedAt: now.UTC(),
		},
		Inputs: inputs,
	}
}

// runsIn reports whether plan p can run in class's pool: a plan that
// encodes no video (a remux copies it) runs in any, one that encodes only in
// its encoder's.
func runsIn(p *Planning, class transcodev1alpha1.Hardware) bool {
	return p.Plan.Video.Action != "encode" || HardwareForEncoder(p.Plan.Video.Encoder) == class
}

// choosesClass reports whether the file chooses its class per dispatch:
// the block's hardware in force, else the winning profile's, is auto or gpu.
func choosesClass(b *catalogv1alpha1.TranscodeState, e Eligibility) bool {
	hw := transcodev1alpha1.Hardware(b.Hardware)
	if hw == "" && e.Profile != nil {
		hw = hardwareOf(e.Profile)
	}
	return hw == "" || hw == transcodev1alpha1.HardwareAuto || hw == transcodev1alpha1.HardwareGPU
}

// unincorporatedFact reports a record answered at b's dispatch with a
// succeeded answer the block has not incorporated -- the file already
// changed on disk -- which a dispatch never goes out over (§4.10).
func unincorporatedFact(rec *task.Record, b *catalogv1alpha1.TranscodeState) bool {
	return rec != nil && task.RecordSpec.Fact(rec) && b.Dispatch != nil && rec.Seq == b.Dispatch.Seq &&
		(b.Dispatch.Withdrawn || b.Dispatch.Seq > b.Dispatch.AnsweredSeq)
}

// taskFor builds b's dispatch task again, for an owed request or a
// republish: the same function and inputs the dispatch used.
func taskFor(in Inputs, b *catalogv1alpha1.TranscodeState) (*task.Task, error) {
	tp := in.profile(b.Profile)
	if tp == nil {
		return nil, errNoProfile
	}
	mf := in.File.DeepCopy()
	mf.Status = *in.Draft
	mf.Status.ProbeHash = b.ProbeHash
	t, err := jobspec.BuildTask(mf, tp, b.ProfileHash, in.Folders, b.Dispatch.Seq, transcodev1alpha1.Hardware(b.Class), b.Plan)
	if err != nil {
		return nil, err
	}
	t.OriginalLanguage = in.OriginalLanguage
	return &t, nil
}
