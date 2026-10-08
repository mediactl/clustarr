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

package graftplan

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/records"
)

// The graft timings (loop spec §4.12, §5.13).
const (
	// DefaultConcurrency is --graft-concurrency's default: standalone
	// grafts running at once per namespace, each a CPU pod of a cpu pool's
	// size.
	DefaultConcurrency = 2
	// JobDeadline is a graft Job's activeDeadlineSeconds: a two-hour film's
	// graft is minutes.
	JobDeadline = 2 * time.Hour
	// JoinWait is the longest a graft waits for its file's transcode, to
	// ride along with it, before it grafts alone.
	JoinWait = 6 * time.Hour
	// FailureBackoff is how long a failure that is not the donor's waits
	// before it is retried.
	FailureBackoff = 15 * time.Minute
	// JobGrace is graftJobGrace: how long a finished Job's answer may lag
	// the Job before the Job counts as lost.
	JobGrace = 2 * time.Minute
	// FactWindow is records.FactWindow for a graft (loop spec §4.10): how
	// long a withdrawn dispatch's record is still read for a fact.
	FactWindow = JobDeadline + JobGrace

	recheck         = time.Minute
	joinWaitRecheck = 30 * time.Minute
)

// Reasons the graft planner writes on status.graft (loop spec §2.6); the
// rest are grafttask's.
const (
	ReasonTranscodeRunning    = "TranscodeRunning"
	ReasonWaitingForTranscode = "WaitingForTranscode"
	ReasonWaitingForSlot      = "WaitingForSlot"
	ReasonBackoff             = "Backoff"
	ReasonJoinedTranscode     = "JoinedTranscode"
	ReasonJobLost             = "JobLost"
)

// View is the loop's view as this planner reads it (remediation.View,
// narrowed).
type View struct {
	File         *catalogv1alpha1.MediaFile
	Prev         *catalogv1alpha1.MediaFileStatus
	Draft        *catalogv1alpha1.MediaFileStatus // after the probe and transcode planners this pass
	Now          metav1.Time
	ProbeCurrent bool
	// Issue is remediation.(*View).Issue; only a dispatch calls it.
	Issue func(recordSeq int64) int64
}

// Item is the file's one item as a graft reads it.
type Item struct {
	UID     string
	Audio   *catalogv1alpha1.AudioState // status.audio: Missing and Donor
	Anchor  string                      // the original language, normalised; "" when unknown
	Grafts  bool                        // the QualityProfile's spec.audio.graft
	Default string                      // spec.audio.default
}

// Job is the graft Job status.graft.jobName names, as the cache has it.
type Job struct {
	Name       string
	Finished   bool
	FinishedAt time.Time
	Message    string
}

// Input is what the adapter gathered for one pass.
type Input struct {
	Item       *Item             // nil: the item or its profile is gone
	Root       string            // the RootFolder spec.path holding the file; "" none
	RecycleBin string            // jobspec.RecycleBinOf(that RootFolder)
	Record     *grafttask.Record // clustarr-grafts, when read (loop spec §4.10); nil when absent
	Transcode  *task.Record      // the transcode record, while a join is unincorporated
	Job        *Job              // nil when the cache has no such Job
	Granted    bool              // a graft-slot grant is held for the file and taken
}

// EffectKind is what one of Plan's effects does.
type EffectKind string

// The effects, run after the apply in §3.8's order.
const (
	EffectRequest   EffectKind = "Request"   // write Record (requested) at the read revision
	EffectWithdraw  EffectKind = "Withdraw"  // write withdrawn{Seq} at the read revision
	EffectCreateJob EffectKind = "CreateJob" // create the Job of Task, named Job
	EffectDeleteJob EffectKind = "DeleteJob" // delete the Job named Job, Background propagation
	EffectConfirm   EffectKind = "Confirm"   // the ledger's graft-slot grant became dispatch Seq
)

// Effect is one write the apply owes.
type Effect struct {
	Kind   EffectKind
	Seq    int64
	Record *grafttask.Record
	Task   *grafttask.Task
	Job    string
}

// Event is one Kubernetes Event on the MediaFile.
type Event struct{ Type, Reason, Message string }

// Decision is Plan's answer beside the status it wrote into out.
type Decision struct {
	Effects []Effect
	Due     time.Time
	Events  []Event
	// Records are the record states this pass incorporated.
	Records []string
	// Unpaced exempts the apply from the status-write limiter (§3.7): a
	// dispatch, an incorporation.
	Unpaced bool
	// Again asks for the next pass in a second: a graft just became
	// Swapping.
	Again bool
}

func (d *Decision) due(t time.Time) {
	if !t.IsZero() && (d.Due.IsZero() || t.Before(d.Due)) {
		d.Due = t
	}
}

// JobName is the standalone graft Job of attempt seq: the dispatch's
// identity, so a re-create is AlreadyExists (§5.13). A label-safe name: a
// Job's name becomes its pods' job-name label.
func JobName(file string, seq int64) string {
	return names.LabelSafeName(file, "graft", strconv.FormatInt(seq, 10))
}

// DonorFault reports a failure that rejects the donor's release (§2.6).
func DonorFault(reason string) bool {
	switch reason {
	case grafttask.ReasonAlignmentRejected, grafttask.ReasonVerifyFailed, grafttask.ReasonMuxFailed, grafttask.ReasonDonorLacksLanguage:
		return true
	}
	return false
}

// Standalone reports a block whose own Job holds its file: Queued, or
// Running and not riding a transcode (§5.13). A transcode keeps off such a
// file.
func Standalone(b *catalogv1alpha1.GraftState) bool {
	if b == nil {
		return false
	}
	return b.Phase == catalogv1alpha1.GraftPhaseQueued ||
		b.Phase == catalogv1alpha1.GraftPhaseRunning && b.Reason != ReasonJoinedTranscode
}

// JoinInFlight reports a transcode block carrying a join for its in-flight
// dispatch (admission's FileView.JoinedGraft).
func JoinInFlight(tc *catalogv1alpha1.TranscodeState) bool {
	return tc != nil && tc.JoinedGraft != nil && tc.Dispatch != nil && tc.Dispatch.InFlight()
}

// FirstMissing is the first of langs the probe lacks (BCP-47 base
// compared), "" when it carries them all or is unknown.
func FirstMissing(langs []string, mi *commonv1.MediaInfo) string {
	if mi == nil {
		return ""
	}
	have := map[string]bool{}
	for _, a := range mi.Audio {
		if t, ok := lang.Normalize(a.Language); ok {
			have[baseTag(string(t))] = true
		}
	}
	for _, l := range langs {
		if t, ok := lang.Normalize(l); ok && !have[baseTag(string(t))] {
			return string(t)
		}
	}
	return ""
}

func baseTag(t string) string {
	b, _, _ := strings.Cut(t, "-")
	return strings.ToLower(b)
}

// isDefault reports whether the grafted language l is the profile's
// default ("original" is the anchor's, never a dub's).
func isDefault(def, l string) bool {
	return def != "" && def != "original" && baseTag(def) == baseTag(l)
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

// Plan is one pass of the graft planner (§4.12's loop rows, §5.13). It
// writes status.graft, status.graftTag and status.graftedAt into out, and
// nothing else.
func Plan(v View, in Input, out *catalogv1alpha1.MediaFileStatus) Decision {
	var d Decision
	out.Graft = v.Prev.Graft.DeepCopy()
	out.GraftTag, out.GraftedAt = v.Prev.GraftTag, v.Prev.GraftedAt.DeepCopy()
	if !v.ProbeCurrent {
		return d // §3.5: every planner after the probe keeps its last block
	}
	plan(v, in, out, &d)
	// A graft that swapped the file this pass is Swapping now; the next
	// pass, the probe planner reads the grafted bytes' probe for a Swapping
	// block (and hands the main apply the spec takeover), and the Swapping
	// row ends it with status.graftTag in that same apply (§3.10).
	if b := out.Graft; b != nil && b.Phase == catalogv1alpha1.GraftPhaseSwapping &&
		(v.Prev.Graft == nil || v.Prev.Graft.Phase != catalogv1alpha1.GraftPhaseSwapping) {
		d.Again = true
	}
	return d
}

func plan(v View, in Input, out *catalogv1alpha1.MediaFileStatus, d *Decision) {
	b := out.Graft
	switch {
	case b != nil && b.Phase == catalogv1alpha1.GraftPhaseSwapping:
		swapped(v, b, out, d)
		return
	case b != nil && b.Phase == catalogv1alpha1.GraftPhaseRunning && b.Reason == ReasonJoinedTranscode:
		joined(v, in, b, d)
		return
	case Standalone(b):
		inFlight(v, in, b, d)
		return
	}
	if b != nil && factOpen(v, b) {
		if r := in.Record; closedAt(r, b.Dispatch.Seq) && grafttask.Fact(r) {
			// A graft that swapped the file after its dispatch was withdrawn
			// (§4.10): the bytes changed, so it is incorporated.
			incorporate(b, *r.Answer, answerTime(r.RecordHeader, v.Now))
			b.Dispatch.Withdrawn = false
			d.Records, d.Unpaced = append(d.Records, r.State), true
			return
		}
		d.due(b.Dispatch.DispatchedAt.Add(FactWindow))
	}
	if tc := v.Draft.Transcode; tc != nil && tc.JoinedGraft != nil && tc.Dispatch != nil &&
		(b == nil && tc.Dispatch.InFlight() || b != nil && b.JoinedTranscodeSeq < tc.Dispatch.Seq) {
		// The transcode planner dispatched with this graft riding along
		// (§5.13): derived here, since status.graft is this planner's alone.
		// With no block to carry it, only while that dispatch is in flight: a
		// join whose transcode ended is not derived again for a file that
		// stopped grafting.
		j, s := tc.JoinedGraft, tc.Dispatch.Seq
		g := &catalogv1alpha1.GraftState{
			Phase: catalogv1alpha1.GraftPhaseRunning, Reason: ReasonJoinedTranscode,
			Message:      fmt.Sprintf("grafting in the same pass as transcode attempt %d", s),
			DonorRelease: j.DonorRelease, DonorImportedAt: j.DonorImportedAt.DeepCopy(), Languages: slices.Clone(j.Languages),
			ProbeHash: j.ProbeHash, JoinedTranscodeSeq: s, StartedAt: ptrTime(v.Now),
		}
		if b != nil {
			g.Dispatch = b.Dispatch.DeepCopy()
		}
		out.Graft = g
		d.Unpaced = true
		return
	}
	if !exists(v, in.Item) {
		if b == nil || !factOpen(v, b) {
			out.Graft = nil // graftTag and graftedAt stay: they describe the bytes (§3.10)
		}
		return
	}
	idle(v, in, out, d)
}

// exists is §2.6's "when the graft block exists": a single-item file whose
// item's profile grafts, whose original language is known, which has a
// donor, and which is missing a language or has a graft result for these
// bytes.
func exists(v View, it *Item) bool {
	if len(v.File.Spec.MediaRef.Keys) > 0 || it == nil || !it.Grafts || it.Anchor == "" || it.Audio == nil || it.Audio.Donor == nil {
		return false
	}
	if len(it.Audio.Missing) > 0 {
		return true
	}
	b := v.Prev.Graft
	return b != nil && b.ProbeHash != "" && b.ProbeHash == v.Draft.ProbeHash
}

// factOpen reports a withdrawn dispatch whose record may still take a fact.
func factOpen(v View, b *catalogv1alpha1.GraftState) bool {
	return b.Dispatch != nil && b.Dispatch.Withdrawn && v.Now.Time.Before(b.Dispatch.DispatchedAt.Add(FactWindow))
}

// closedAt reports a record answered or failed at s.
func closedAt(r *grafttask.Record, s int64) bool {
	return r != nil && r.Seq == s && (r.State == records.StateAnswered || r.State == records.StateFailed)
}

func answerTime(h schema.RecordHeader, now metav1.Time) metav1.Time {
	if h.AnsweredAt != nil {
		return metav1.NewTime(h.AnsweredAt.UTC().Truncate(time.Second))
	}
	return now
}

// swapped is the Swapping row: the grafted bytes' probe, once it lands,
// ends the graft and gives the file its graftTag (§4.12).
func swapped(v View, b *catalogv1alpha1.GraftState, out *catalogv1alpha1.MediaFileStatus, d *Decision) {
	p := v.Draft
	if p.ProbeHash == "" || p.ProbeHash == b.ProbeHash || p.ProbedAt == nil ||
		(b.CompletedAt != nil && b.CompletedAt.After(p.ProbedAt.Time)) {
		d.due(v.Now.Add(recheck))
		return
	}
	b.Phase, b.ProbeHash = catalogv1alpha1.GraftPhaseSucceeded, p.ProbeHash
	out.GraftTag, out.GraftedAt = b.Tag, ptrTime(v.Now)
	d.Unpaced = true
	d.Events = append(d.Events, Event{Type: "Normal", Reason: grafttask.ReasonGrafted, Message: "the grafted dub is in the file (" + b.Tag + ")"})
}

// incorporate folds a graft result into b (§4.12 "record answered").
func incorporate(b *catalogv1alpha1.GraftState, res grafttask.Result, at metav1.Time) {
	b.CompletedAt = &at
	b.RateName, b.RateMicros, b.RateMarginMilli = res.RateName, res.RateMicros, res.RateMarginMilli
	b.CoveragePercent, b.ResidualMillis, b.Within80Percent = res.CoveragePercent, res.ResidualMillis, res.Within80Percent
	b.Segments = nil
	for i, s := range res.Segments {
		if i == 16 {
			break
		}
		b.Segments = append(b.Segments, catalogv1alpha1.GraftSegment(s))
	}
	b.Reason, b.Message, b.DonorFault, b.JobName = res.Reason, grafttask.Clamp(res.Message), false, ""
	switch {
	case res.Phase == grafttask.PhaseSucceeded && res.Reason == grafttask.ReasonGrafted:
		b.Phase, b.Tag = catalogv1alpha1.GraftPhaseSwapping, res.GraftTag
	case res.Phase == grafttask.PhaseSucceeded:
		b.Phase = catalogv1alpha1.GraftPhaseSucceeded
	case DonorFault(res.Reason):
		b.Phase, b.DonorFault = catalogv1alpha1.GraftPhaseFailed, true
	default:
		b.Phase, b.Reason = catalogv1alpha1.GraftPhaseWaiting, ReasonBackoff
		b.Message = grafttask.Clamp(res.Reason + ": " + res.Message)
	}
}

// joined is the joined-graft rows: the transcode record's Answer.Graft is
// this graft's result (§5.13).
func joined(v View, in Input, b *catalogv1alpha1.GraftState, d *Decision) {
	s, r := b.JoinedTranscodeSeq, in.Transcode
	switch {
	case r != nil && r.Seq == s && r.State == records.StateAnswered && r.Answer != nil && r.Answer.Graft != nil:
		incorporate(b, *r.Answer.Graft, answerTime(r.RecordHeader, v.Now))
		d.Records, d.Unpaced = append(d.Records, r.State), true
	case r != nil && (r.Seq > s || r.Seq == s && (r.State == records.StateAnswered || r.State == records.StateFailed || r.State == records.StateWithdrawn)),
		r == nil && !joinOpen(v.Draft.Transcode, s):
		b.Phase, b.Reason, b.Message = catalogv1alpha1.GraftPhaseWaiting, ReasonWaitingForSlot,
			fmt.Sprintf("transcode attempt %d ended without its graft", s)
		b.CompletedAt = ptrTime(v.Now)
	default:
		d.due(v.Now.Add(recheck))
	}
}

// joinOpen reports the transcode block still carrying the join of dispatch
// s in flight.
func joinOpen(tc *catalogv1alpha1.TranscodeState, s int64) bool {
	return tc != nil && tc.JoinedGraft != nil && tc.Dispatch != nil && tc.Dispatch.Seq == s && tc.Dispatch.InFlight()
}

// inFlight is the standalone rows: record, Job, withdrawal (§4.12, §5.10).
func inFlight(v View, in Input, b *catalogv1alpha1.GraftState, d *Decision) {
	if b.Dispatch == nil {
		b.Phase, b.Reason, b.Message = catalogv1alpha1.GraftPhaseWaiting, ReasonWaitingForSlot, "an in-flight graft without a dispatch"
		return
	}
	s, r, now, job := b.Dispatch.Seq, in.Record, v.Now.Time, b.JobName
	switch {
	case r != nil && r.Seq < s && r.State == records.StateAnswered && grafttask.Fact(r) && r.Seq > b.Dispatch.AnsweredSeq:
		// The earlier dispatch's fact under this one's missed request
		// (§4.10): nothing ran at s; the fact is incorporated.
		b.Dispatch.AnsweredSeq = s
		incorporate(b, *r.Answer, answerTime(r.RecordHeader, v.Now))
		d.Records, d.Unpaced = append(d.Records, r.State), true
		d.Effects = append(d.Effects, Effect{Kind: EffectDeleteJob, Job: job})
	case closedAt(r, s):
		if r.State == records.StateFailed || r.Answer == nil {
			at := answerTime(r.RecordHeader, v.Now)
			b.Phase, b.Reason, b.CompletedAt, b.JobName = catalogv1alpha1.GraftPhaseWaiting, ReasonBackoff, &at, ""
			b.Message = grafttask.Clamp(cmp.Or(r.Failure, "the graft pod gave no answer"))
		} else {
			incorporate(b, *r.Answer, answerTime(r.RecordHeader, v.Now))
		}
		b.Dispatch.AnsweredSeq = s
		d.Records, d.Unpaced = append(d.Records, r.State), true
		d.Effects = append(d.Effects, Effect{Kind: EffectDeleteJob, Job: job})
		if b.Phase == catalogv1alpha1.GraftPhaseWaiting {
			d.due(b.CompletedAt.Add(FailureBackoff))
		}
	case r != nil && r.Seq == s && r.State == records.StateWithdrawn:
		b.Phase, b.Reason, b.CompletedAt = catalogv1alpha1.GraftPhaseFailed, ReasonJobLost, ptrTime(v.Now)
		b.Message = lostMessage(in.Job, s)
		b.Dispatch.AnsweredSeq, b.Dispatch.Withdrawn, b.JobName = s, true, ""
		d.Effects = append(d.Effects, Effect{Kind: EffectDeleteJob, Job: job})
		d.due(now.Add(FailureBackoff))
	case r != nil && r.Seq > s:
		// A newer request owns the key: nothing this block dispatched can
		// answer it. Withdrawing at s writes nothing over it; fail as lost.
		b.Phase, b.Reason, b.CompletedAt = catalogv1alpha1.GraftPhaseFailed, ReasonJobLost, ptrTime(v.Now)
		b.Message = grafttask.Clamp(fmt.Sprintf("graft attempt %d: the record moved on to %d", s, r.Seq))
		b.Dispatch.AnsweredSeq, b.JobName = s, ""
		d.Effects = append(d.Effects, Effect{Kind: EffectDeleteJob, Job: job})
		d.due(now.Add(FailureBackoff))
	case b.Reason == ReasonJobLost || jobLost(in, r, s, now):
		// Withdraw first, fail on the pass that reads the withdrawal: a
		// pod still answering gets its answer in, or none (§5.10).
		b.Reason, b.Message = ReasonJobLost, fmt.Sprintf("withdrawing attempt %d", s)
		d.Effects = append(d.Effects, Effect{Kind: EffectWithdraw, Seq: s})
		d.due(now.Add(time.Second))
	case r != nil && r.Seq == s && r.State == records.StateClaimed:
		if b.Phase != catalogv1alpha1.GraftPhaseRunning {
			d.Records, d.Unpaced = append(d.Records, r.State), true
		}
		b.Phase = catalogv1alpha1.GraftPhaseRunning
		if r.ClaimedAt != nil {
			b.StartedAt = ptrTime(metav1.NewTime(r.ClaimedAt.UTC().Truncate(time.Second)))
		}
		b.Message = grafttask.Clamp("graft pod " + r.Writer)
		d.due(now.Add(recheck))
		jobDue(in.Job, d)
	default: // absent, below s, or requested at s: §4.7's owed effects
		if r == nil || r.Seq < s {
			d.Effects = append(d.Effects, Effect{Kind: EffectRequest, Seq: s, Record: requested(v, b, inputsOf(v, b, in))})
		}
		if in.Job == nil {
			if t, ok := owedTask(v, in, b); ok {
				d.Effects = append(d.Effects, Effect{Kind: EffectCreateJob, Seq: s, Task: &t, Job: job})
			}
		}
		d.due(now.Add(recheck))
		jobDue(in.Job, d)
	}
}

// jobLost reports a dispatch whose Job is gone while its record says
// claimed, or finished past JobGrace with no answer at s.
func jobLost(in Input, r *grafttask.Record, s int64, now time.Time) bool {
	if closedAt(r, s) {
		return false
	}
	if in.Job == nil {
		return r != nil && r.Seq == s && r.State == records.StateClaimed // claimed, and its Job is gone
	}
	return in.Job.Finished && !now.Before(in.Job.FinishedAt.Add(JobGrace))
}

func jobDue(j *Job, d *Decision) {
	if j != nil && j.Finished {
		d.due(j.FinishedAt.Add(JobGrace))
	}
}

func lostMessage(j *Job, s int64) string {
	msg := fmt.Sprintf("graft attempt %d: the Job ended without an answer", s)
	if j != nil && j.Message != "" {
		msg += ": " + j.Message
	}
	return grafttask.Clamp(msg)
}

// inputsOf are the inputs of b's dispatch as this pass reads them.
func inputsOf(v View, b *catalogv1alpha1.GraftState, in Input) grafttask.Inputs {
	x := grafttask.Inputs{TargetProbeHash: b.ProbeHash, DonorRelease: b.DonorRelease, Language: FirstMissing(b.Languages, v.Draft.MediaInfo)}
	if it := in.Item; it != nil {
		x.Anchor = it.Anchor
		if it.Audio != nil && it.Audio.Donor != nil {
			x.DonorAudio = it.Audio.Donor.Path
		}
	}
	return x
}

func fileRef(f *catalogv1alpha1.MediaFile) schema.Ref {
	return schema.Ref{Namespace: f.Namespace, Name: f.Name, UID: string(f.UID)}
}

// requested is the record of b's dispatch, requested.
func requested(v View, b *catalogv1alpha1.GraftState, x grafttask.Inputs) *grafttask.Record {
	return &grafttask.Record{RecordHeader: schema.RecordHeader{
		Schema: grafttask.RecordSchema, MediaFile: fileRef(v.File),
		Seq: b.Dispatch.Seq, State: records.StateRequested, RequestedAt: b.Dispatch.DispatchedAt.UTC(),
	}, Inputs: x}
}

// owedTask is the task of b's dispatch, rebuilt from its record's inputs
// when the record holds them.
func owedTask(v View, in Input, b *catalogv1alpha1.GraftState) (grafttask.Task, bool) {
	if in.Item == nil || in.Root == "" {
		return grafttask.Task{}, false
	}
	x := inputsOf(v, b, in)
	if r := in.Record; r != nil && r.Seq == b.Dispatch.Seq {
		x = r.Inputs
	}
	if x.Language == "" || x.DonorAudio == "" || x.Anchor == "" {
		return grafttask.Task{}, false
	}
	return taskOf(v, in, b.Dispatch.Seq, x, b.Languages), true
}

// taskOf is the standalone graft task of dispatch s.
func taskOf(v View, in Input, s int64, x grafttask.Inputs, langs []string) grafttask.Task {
	return grafttask.Task{
		Schema: grafttask.TaskSchema, File: fileRef(v.File), Item: in.Item.UID, Seq: s, Inputs: x,
		Target: v.File.Spec.Path, TargetProbeHash: x.TargetProbeHash, Root: in.Root, Donor: x.DonorAudio,
		Language: x.Language, Languages: slices.Clone(langs), Anchor: x.Anchor,
		Default: isDefault(in.Item.Default, x.Language), RecycleBin: in.RecycleBin,
	}
}

// sameAttempt reports a block for this donor and these bytes.
func sameAttempt(b *catalogv1alpha1.GraftState, donor *catalogv1alpha1.AudioDonor, probe string) bool {
	return b.DonorRelease == donor.Release && b.ProbeHash == probe && b.DonorImportedAt != nil && b.DonorImportedAt.Equal(&donor.ImportedAt)
}

func waitingOf(base catalogv1alpha1.GraftState, reason, msg string) *catalogv1alpha1.GraftState {
	base.Phase, base.Reason, base.Message = catalogv1alpha1.GraftPhaseWaiting, reason, msg
	return &base
}

// idle decides for a file with no graft in flight: Present, a verdict that
// stands, waiting for the file's transcode, for a slot, or a dispatch.
func idle(v View, in Input, out *catalogv1alpha1.MediaFileStatus, d *Decision) {
	b, it, now := out.Graft, in.Item, v.Now.Time
	if v.Draft.MediaInfo == nil {
		return
	}
	donor := it.Audio.Donor
	l := FirstMissing(it.Audio.Missing, v.Draft.MediaInfo)
	base := catalogv1alpha1.GraftState{
		DonorRelease: donor.Release, DonorImportedAt: donor.ImportedAt.DeepCopy(),
		Languages: slices.Clone(it.Audio.Missing), ProbeHash: v.Draft.ProbeHash,
	}
	if b != nil {
		base.Dispatch, base.JoinedTranscodeSeq = b.Dispatch.DeepCopy(), b.JoinedTranscodeSeq
	}
	if l == "" {
		if b != nil && b.Phase == catalogv1alpha1.GraftPhaseSucceeded && b.ProbeHash == v.Draft.ProbeHash {
			return
		}
		p := base
		p.Phase, p.Reason, p.CompletedAt = catalogv1alpha1.GraftPhaseSucceeded, grafttask.ReasonPresent, ptrTime(v.Now)
		p.Message = grafttask.Clamp("the file carries " + strings.Join(it.Audio.Missing, ", "))
		out.Graft = &p
		return
	}
	if b != nil && sameAttempt(b, donor, v.Draft.ProbeHash) {
		switch {
		case b.Phase == catalogv1alpha1.GraftPhaseFailed && b.DonorFault:
			return // final for this donor and these bytes (§2.6)
		case b.Phase == catalogv1alpha1.GraftPhaseSucceeded:
			// The worker found the language present (or grafted it) where the
			// probe sees none: the verdict stands for this donor and these
			// bytes, never a graft Job per slot.
			return
		case b.Phase == catalogv1alpha1.GraftPhaseFailed, b.Phase == catalogv1alpha1.GraftPhaseWaiting && b.Reason == ReasonBackoff:
			if b.CompletedAt != nil {
				if retry := b.CompletedAt.Add(FailureBackoff); now.Before(retry) {
					d.due(retry)
					return
				}
			}
		}
	}
	if tc := v.Draft.Transcode; tc != nil {
		switch tc.Phase {
		case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
			// Never a graft beside a transcode of the same file (spec §7.2).
			out.Graft = waitingOf(base, ReasonTranscodeRunning, "the file's transcode is in flight; the graft follows it")
			d.due(now.Add(recheck))
			return
		case catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned:
			// A transcode is due: wait for it, to graft in the same pass,
			// at most JoinWait (phase 4 addendum).
			since := v.Now
			if b != nil && b.Phase == catalogv1alpha1.GraftPhaseWaiting && b.Reason == ReasonWaitingForTranscode && b.StartedAt != nil {
				since = *b.StartedAt
			}
			if until := since.Add(JoinWait); now.Before(until) {
				w := waitingOf(base, ReasonWaitingForTranscode, "waiting for the file's transcode, to graft in the same pass")
				w.StartedAt = &since
				out.Graft = w
				d.due(until)
				d.due(now.Add(joinWaitRecheck))
				return
			}
		}
	}
	if in.Root == "" {
		f := base
		f.Phase, f.Reason, f.CompletedAt = catalogv1alpha1.GraftPhaseFailed, grafttask.ReasonInvalidTask, ptrTime(v.Now)
		f.Message = grafttask.Clamp("the file " + v.File.Spec.Path + " is under no RootFolder")
		out.Graft = &f
		return
	}
	if !in.Granted {
		w := waitingOf(base, ReasonWaitingForSlot, "waiting for a graft slot")
		if b != nil && b.Phase == catalogv1alpha1.GraftPhaseWaiting && b.Reason == ReasonWaitingForSlot {
			w.Message = b.Message // the slot wait is written once, not on every pass
		}
		out.Graft = w
		return
	}
	var recSeq int64
	if in.Record != nil {
		recSeq = in.Record.Seq
	}
	s := v.Issue(recSeq)
	x := grafttask.Inputs{TargetProbeHash: v.Draft.ProbeHash, DonorAudio: donor.Path, DonorRelease: donor.Release, Language: l, Anchor: it.Anchor}
	t := taskOf(v, in, s, x, it.Audio.Missing)
	q := base
	q.Phase, q.Reason, q.JobName = catalogv1alpha1.GraftPhaseQueued, "", JobName(v.File.Name, s)
	q.Message = fmt.Sprintf("attempt %d: graft Job %s", s, q.JobName)
	q.Dispatch = &catalogv1alpha1.Dispatch{Seq: s, DispatchedAt: v.Now}
	if base.Dispatch != nil && !base.Dispatch.Withdrawn {
		// What the block incorporated through. A withdrawn dispatch was
		// closed, not incorporated: its fact may still land under this
		// request and must be incorporated (§4.7, §4.10).
		q.Dispatch.AnsweredSeq = base.Dispatch.AnsweredSeq
	}
	q.StartedAt, q.CompletedAt = nil, nil
	out.Graft = &q
	rec := &grafttask.Record{RecordHeader: schema.RecordHeader{
		Schema: grafttask.RecordSchema, MediaFile: t.File, Seq: s, State: records.StateRequested, RequestedAt: now.UTC(),
	}, Inputs: x}
	d.Unpaced = true
	d.Effects = append(d.Effects,
		Effect{Kind: EffectRequest, Seq: s, Record: rec},
		Effect{Kind: EffectCreateJob, Seq: s, Task: &t, Job: q.JobName},
		Effect{Kind: EffectConfirm, Seq: s})
	d.due(now.Add(recheck))
	d.Events = append(d.Events, Event{Type: "Normal", Reason: "Dispatched", Message: fmt.Sprintf("graft attempt %d: %s from %s", s, l, donor.Release)})
}
