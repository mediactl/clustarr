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

package importplan

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/records"
)

// Durable is the import tasks' durable.
const Durable = events.ConsumerImportFile

// RepublishEvery is how often an unanswered task is republished under its
// Msg-Id inside records.RepublishWindow (a lost publish, a leader change);
// past the window it is re-issued at a new seq.
const RepublishEvery = 5 * time.Minute

// materialiseRecheck is how soon a pass looks again for the MediaFiles an
// import materialised.
const materialiseRecheck = 10 * time.Second

// DonorItem is what a donor import needs of the Episode or Movie it is for.
type DonorItem struct {
	Item schema.ItemRef
	// Missing are the languages the item lacks (status.audio.missing),
	// Original its original language (the anchor).
	Missing  []string
	Original string
	// AudioDefault is the profile's audio default.
	AudioDefault string
	// GraftDonor is the item's AudioGraft's spec.donorPath, "" for none.
	GraftDonor string
}

// Input is one Completed entry's import, as the downloads stage gathered
// it.
type Input struct {
	Owner     schema.ItemRef
	OwnerKind commonv1.MediaKind
	Entry     catalogv1alpha1.DownloadEntry
	// Transfer is the entry's transfer record, nil when it has none.
	Transfer *schema.TransferRecord
	// Inspect and Execute are the entry's import records, whatever their
	// seq; nil when absent.
	Inspect, Execute *schema.ImportRecord
	// Existing is each covered item's current MediaFiles, by
	// mfindex.ItemKey of every key the inspection's files name.
	Existing map[string][]catalogv1alpha1.MediaFile
	// Named is the MediaFiles that exist under the names the execute record
	// placed.
	Named map[string]*catalogv1alpha1.MediaFile
	// Targets are the items the entry covers (its Episodes or Issues), or
	// the owner.
	Targets []schema.ItemRef
	// ProfileRef names the quality profile the grab was decided under;
	// Profile is it resolved, nil with ProfileError when it cannot be.
	ProfileRef   string
	Profile      *quality.Profile
	ProfileError string
	// OriginalLanguage is the owner's original language tag.
	OriginalLanguage string
	// Donor is set for an audioDonor entry.
	Donor *DonorItem
	// Intent is a pending download.clustarr.io/import intent naming this
	// entry.
	Intent *lifecycle.ImportIntent
	// Files counts the MediaFiles naming the entry's id.
	Files int
	Now   time.Time
	// Admit asks the dispatch ledger for seq; nil admits.
	Admit func(seq int64) dispatch.Decision
	// Published is when the stage last published the in-flight seq (its
	// leader-local book), zero when it does not know.
	Published time.Time
}

// Decide is one Completed entry's import decision (ADR-0019 §6.9).
func Decide(in Input) lifecycle.ImportDecision {
	p := &planner{in: in, now: in.Now.UTC()}
	if in.Entry.Import != nil {
		p.sum = *in.Entry.Import.DeepCopy()
	} else {
		p.sum = catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhasePending}
	}
	p.dec.Verdict = lifecycle.ImportNone
	p.decide()
	p.sum.Message = k8s.ClampText(p.sum.Message, catalogv1alpha1.MaxEntryMessage)
	p.dec.Summary = p.sum
	return p.dec
}

type planner struct {
	in  Input
	now time.Time
	sum catalogv1alpha1.DownloadImportSummary
	dec lifecycle.ImportDecision
}

func (p *planner) at(t time.Time) {
	if t.IsZero() {
		return
	}
	if p.dec.Due.IsZero() || t.Before(p.dec.Due) {
		p.dec.Due = t
	}
}

func (p *planner) manual() bool { return p.in.Entry.Manual || p.sum.Override }

func (p *planner) donor() bool { return p.in.Entry.Purpose == commonv1.DownloadPurposeAudioDonor }

func (p *planner) decide() {
	if it := p.in.Intent; it != nil {
		// P52: the intent redirects the target and marks override; the
		// import starts over from a fresh inspect.
		p.sum.Target, p.sum.Override = TargetString(it), it.Override
		p.sum.Attempts, p.sum.Class, p.sum.HeldSince, p.sum.NextAttemptAt = 0, "", nil, nil
		p.sum.Message = "imported by hand: inspecting again"
		p.inspect()
		return
	}
	switch p.sum.Phase {
	case catalogv1alpha1.ImportPhaseExpired:
		p.dec.Verdict = lifecycle.ImportExpired
	case catalogv1alpha1.ImportPhaseBlocked:
		p.dec.Verdict = lifecycle.ImportReleaseFault
	case catalogv1alpha1.ImportPhaseHeld:
		p.held()
	case catalogv1alpha1.ImportPhaseImported:
		p.imported()
	case catalogv1alpha1.ImportPhaseApproved:
		p.approved()
	default:
		p.pending()
	}
}

// held keeps a held import until ImportHoldRetention, then expires it.
func (p *planner) held() {
	if p.sum.HeldSince == nil {
		t := metav1.NewTime(p.now)
		p.sum.HeldSince = &t
	}
	until := p.sum.HeldSince.UTC().Add(lifecycle.ImportHoldRetention)
	if !p.now.Before(until) {
		p.sum.Phase = catalogv1alpha1.ImportPhaseExpired
		p.dec.Verdict = lifecycle.ImportExpired
		return
	}
	p.dec.Verdict = lifecycle.ImportHeld
	p.at(until)
}

// pending is an inspect in flight, or an answered one waiting to retry.
func (p *planner) pending() {
	d := p.sum.Dispatch
	switch {
	case d.Seq == 0:
		p.inspect()
		return
	case d.InFlight():
		rec := p.in.Inspect
		if rec != nil && rec.Seq >= d.Seq {
			p.answer(d.Seq)
			if rec.State == records.StateFailed || rec.Inspect == nil {
				p.conclude(commonv1.ImportClassTransient, "the payload could not be inspected: "+rec.Failure)
				return
			}
			p.plan(rec.Inspect)
			return
		}
		p.owe(schema.ImportSubInspect)
		return
	}
	if t := p.sum.NextAttemptAt; t != nil {
		if p.now.Before(t.UTC()) {
			p.dec.Verdict = lifecycle.ImportRetry
			p.at(t.UTC())
			return
		}
		p.inspect()
		return
	}
	if rec := p.in.Inspect; rec != nil && rec.Seq == d.AnsweredSeq && rec.Inspect != nil {
		p.plan(rec.Inspect)
		return
	}
	p.inspect()
}

// approved is an execute in flight, or answered.
func (p *planner) approved() {
	d := p.sum.Dispatch
	if d.InFlight() {
		if rec := p.in.Execute; rec != nil && rec.Seq >= d.Seq {
			p.answer(d.Seq)
			p.executed(rec)
			return
		}
		if p.in.Inspect == nil || p.in.Inspect.Inspect == nil {
			// The plan's inspection is gone (a recreated clustarr-imports):
			// inspect again, which finds any file the execute placed.
			p.inspect()
			return
		}
		p.owe(schema.ImportSubExecute)
		return
	}
	if rec := p.in.Execute; rec != nil && rec.Seq == d.AnsweredSeq {
		p.executed(rec)
		return
	}
	p.inspect()
}

// answer closes the in-flight dispatch at seq: the stage tells the ledger.
func (p *planner) answer(seq int64) {
	p.sum.Dispatch.AnsweredSeq = seq
	p.sum.Dispatch.Delivery = nil
	p.dec.Answered = seq
}

// recordSeq is the highest seq either import record carries.
func (p *planner) recordSeq() int64 {
	var n int64
	for _, r := range []*schema.ImportRecord{p.in.Inspect, p.in.Execute} {
		if r != nil {
			n = max(n, r.Seq)
		}
	}
	return n
}

// issue starts a new dispatch for phase at a new seq.
func (p *planner) issue() int64 {
	seq := lifecycle.NextSeq(p.recordSeq(), p.sum.Dispatch.Seq, p.now)
	p.sum.Dispatch = catalogv1alpha1.Dispatch{
		Seq: seq, AnsweredSeq: p.sum.Dispatch.AnsweredSeq, DispatchedAt: metav1.NewTime(p.now),
	}
	return seq
}

// inspect issues an inspect task (P51; again on an intent, a stale plan, a
// due retry or a lost record).
func (p *planner) inspect() {
	seq := p.issue()
	p.sum.Phase = catalogv1alpha1.ImportPhasePending
	p.sum.NextAttemptAt, p.sum.Files = nil, 0
	p.publish(schema.ImportSubInspect, p.inspectTask(seq), false)
}

// inspectTask renders the inspect task at seq from the entry, its transfer
// record and the summary's target and override.
func (p *planner) inspectTask(seq int64) *schema.ImportInspectTask {
	e := &p.in.Entry
	t := &schema.ImportInspectTask{
		Seq: seq, Owner: p.in.Owner, Entry: schema.EntryRef{ID: e.ID, UID: e.UID},
		Release: schema.CommandRelease{
			Title: e.Release.Title, GUID: e.Release.GUID, IndexerRef: e.Release.IndexerRef, SizeBytes: e.Release.SizeBytes,
		},
		Protocol: e.Release.Protocol, Targets: p.in.Targets, Issues: e.Issues,
		QualityProfile: p.in.ProfileRef, Purpose: e.Purpose, GrabbedBy: e.GrabbedBy,
		Manual: e.Manual, Override: p.sum.Override, TargetOverride: ParseTarget(p.sum.Target),
		OutputPath: e.OutputPath,
	}
	for _, n := range e.Episodes {
		t.Episodes = append(t.Episodes, schema.EpisodeNo{Season: n.Season, Number: n.Number})
	}
	if r := p.in.Transfer; r != nil {
		t.ContentRoot, t.Files = r.ContentRoot, r.Files
		if t.OutputPath == "" {
			t.OutputPath = r.OutputPath
		}
	}
	return t
}

// publish owes phase's task at the summary's seq, admitted by the ledger.
func (p *planner) publish(phase string, task any, republish bool) {
	d := &p.sum.Dispatch
	if p.in.Admit != nil {
		if dec := p.in.Admit(d.Seq); !dec.Admitted {
			d.Delivery = &catalogv1alpha1.DeliveryState{
				State:   catalogv1alpha1.DeliveryWaiting,
				Reason:  k8s.ClampText(dec.Reason, 64),
				Message: k8s.ClampText(dec.Message, 256),
			}
			p.at(p.now.Add(max(dec.RetryAfter, time.Second)))
			return
		}
	}
	uid := p.in.Entry.UID
	subject := events.WorkImportInspectSubject(uid)
	if phase == schema.ImportSubExecute {
		subject = events.WorkImportExecuteSubject(uid)
	}
	p.dec.Dispatch = &lifecycle.ImportDispatch{
		Phase: phase, Seq: d.Seq, Subject: subject, MsgID: events.MsgIDForImport(uid, phase, d.Seq),
		Task: task, Republish: republish,
	}
	d.Destination = Durable
	if d.Delivery == nil || d.Delivery.State == catalogv1alpha1.DeliveryWaiting {
		d.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryPublished}
	}
	p.at(p.now.Add(RepublishEvery))
}

// owe keeps an unanswered task delivered: republished under its Msg-Id
// inside RepublishWindow (when the stage's book says it is due), re-issued
// at a new seq past it. A missing record is never a verdict.
func (p *planner) owe(phase string) {
	d := p.sum.Dispatch
	if !p.now.Before(d.DispatchedAt.UTC().Add(records.RepublishWindow)) {
		if phase == schema.ImportSubExecute {
			p.plan(p.in.Inspect.Inspect)
			return
		}
		p.inspect()
		return
	}
	p.at(d.DispatchedAt.UTC().Add(records.RepublishWindow))
	waiting := d.Delivery != nil && d.Delivery.State == catalogv1alpha1.DeliveryWaiting
	if !waiting && !p.in.Published.IsZero() && p.now.Sub(p.in.Published) < RepublishEvery {
		p.at(p.in.Published.Add(RepublishEvery))
		return
	}
	if phase == schema.ImportSubInspect {
		p.publish(phase, p.inspectTask(d.Seq), true)
		return
	}
	res := p.planFrom(p.in.Inspect.Inspect)
	if !res.ok {
		p.reject(res)
		return
	}
	p.publish(phase, &schema.ImportExecuteTask{
		Seq: d.Seq, Owner: p.in.Owner, Entry: schema.EntryRef{ID: p.in.Entry.ID, UID: p.in.Entry.UID}, Plan: res.plan,
	}, true)
}

// plan decides an answered inspection: an approved plan issues the execute,
// anything else concludes by its class.
func (p *planner) plan(insp *schema.ImportInspection) {
	res := p.planFrom(insp)
	if !res.ok {
		p.reject(res)
		return
	}
	seq := p.issue()
	p.sum.Phase = catalogv1alpha1.ImportPhaseApproved
	p.sum.Class = ""
	p.sum.Files = int32(len(res.plan.Moves)) //nolint:gosec // bounded by the inspection's files
	p.sum.Message = res.note
	p.publish(schema.ImportSubExecute, &schema.ImportExecuteTask{
		Seq: seq, Owner: p.in.Owner, Entry: schema.EntryRef{ID: p.in.Entry.ID, UID: p.in.Entry.UID}, Plan: res.plan,
	}, false)
}

// reject concludes a plan that imports nothing.
func (p *planner) reject(res planResult) {
	p.conclude(res.class, res.message)
}

// executed incorporates an answered execute: a refusal concludes or
// re-inspects; placed files are materialised.
func (p *planner) executed(rec *schema.ImportRecord) {
	x := rec.Execute
	if rec.State == records.StateFailed || x == nil {
		p.conclude(commonv1.ImportClassTransient, "the import could not be carried out: "+rec.Failure)
		return
	}
	switch x.Refused {
	case "":
	case schema.ImportRefusedStale:
		p.sum.Message = "a file the plan replaces changed since it was made: inspecting again"
		p.inspect()
		return
	case schema.ImportRefusedDiskFull:
		p.conclude(commonv1.ImportClassTransient, "the library volume has no room: "+rec.Failure)
		return
	case schema.ImportRefusedOverwrite, schema.ImportRefusedOutsideRoot:
		p.conclude(commonv1.ImportClassNeedsPerson, rec.Failure)
		return
	default:
		p.conclude(commonv1.ImportClassTransient, "the import was refused: "+x.Refused)
		return
	}
	p.sum.Phase = catalogv1alpha1.ImportPhaseImported
	p.imported()
}

// imported materialises what the execute placed (A3.8 step 4), until
// lifecycle sees every MediaFile name the entry.
func (p *planner) imported() {
	rec := p.in.Execute
	if rec == nil || rec.Execute == nil || rec.Seq < p.sum.Dispatch.Seq {
		// The execute record is gone: what it placed is on disk, where a
		// fresh inspect finds it.
		p.inspect()
		return
	}
	x := rec.Execute
	at := metav1.NewTime(p.now)
	if rec.AnsweredAt != nil {
		at = metav1.NewTime(rec.AnsweredAt.UTC().Truncate(time.Second))
	}
	if p.donor() {
		p.materialiseDonor(x)
		return
	}
	from := importSource(&p.in.Entry, p.manual(), at)
	for _, f := range x.Placed {
		ref := mediaRef(f.Target, f.Keys, f.Frozen.Track)
		rv := ""
		if live := p.in.Named[f.MediaFileName]; live != nil {
			if live.Spec.MediaRef.Kind != ref.Kind || live.Spec.MediaRef.Name != ref.Name {
				p.conclude(commonv1.ImportClassNeedsPerson, fmt.Sprintf("media file %s already belongs to %s %s",
					f.MediaFileName, live.Spec.MediaRef.Kind, live.Spec.MediaRef.Name))
				return
			}
			rv = live.ResourceVersion
		}
		p.dec.Materialise = append(p.dec.Materialise, lifecycle.Materialise{Apply: &lifecycle.ApplyMediaFile{
			Name: f.MediaFileName, RV: rv, Ref: ref, Path: f.Dest, SizeBytes: f.SizeBytes, ModTime: f.ModTime,
			Frozen: f.Frozen, From: from,
		}})
	}
	for i := range x.Replaced {
		b := x.Replaced[i]
		p.dec.Materialise = append(p.dec.Materialise, lifecycle.Materialise{Delete: &b})
	}
	p.sum.Files = int32(len(x.Placed)) //nolint:gosec // bounded by the plan
	p.sum.Class = ""
	p.dec.Verdict = lifecycle.ImportImported
	if p.in.Files < len(x.Placed) {
		p.at(p.now.Add(materialiseRecheck))
	}
}

// materialiseDonor names the placed donor in the item's AudioGraft (R26):
// imported once the AudioGraft names it.
func (p *planner) materialiseDonor(x *schema.ImportExecution) {
	d := p.in.Donor
	if d == nil || x.DonorPath == "" {
		p.conclude(commonv1.ImportClassItemState, "the donor's item is gone")
		return
	}
	anchor, _ := lang.Normalize(d.Original)
	def := d.AudioDefault
	if def == "original" {
		def = ""
	}
	old := ""
	if d.GraftDonor != x.DonorPath {
		old = d.GraftDonor
	}
	p.dec.Materialise = append(p.dec.Materialise, lifecycle.Materialise{AudioGraft: &lifecycle.AudioGraftApply{
		Item: d.Item, DonorPath: x.DonorPath, Anchor: string(anchor), Default: def,
		Languages: d.Missing, Release: p.in.Entry.Release.Title, OldDonorPath: old,
	}})
	p.sum.Files = 0
	if d.GraftDonor == x.DonorPath {
		p.dec.Verdict = lifecycle.ImportImported
		return
	}
	p.at(p.now.Add(materialiseRecheck))
}

// conclude settles an import that imports nothing, by its class (P67,
// P68): transient retries at RetryLadder by attempt, then holds; a release
// fault is inspected once more, then blocked; itemState and needsPerson hold
// at once. Attempts and nextAttemptAt are status, not stream state.
func (p *planner) conclude(class commonv1.ImportRejectionClass, message string) {
	attempts := p.sum.Attempts + 1
	p.sum.Attempts = min(attempts, 1000)
	p.sum.Class, p.sum.Files = class, 0
	p.dec.Class = class
	switch {
	case class == commonv1.ImportClassTransient && int(attempts) <= len(RetryLadder):
		p.retry(RetryLadder[attempts-1], message, "retrying")
	case class == commonv1.ImportClassReleaseFault && attempts < 2:
		p.retry(RetryLadder[0], message, "inspecting it once more before the release is blocklisted")
	case class == commonv1.ImportClassReleaseFault:
		p.sum.Phase = catalogv1alpha1.ImportPhaseBlocked
		p.sum.Message = message
		p.dec.Verdict = lifecycle.ImportReleaseFault
	default:
		t := metav1.NewTime(p.now)
		until := p.now.Add(lifecycle.ImportHoldRetention)
		p.sum.Phase = catalogv1alpha1.ImportPhaseHeld
		p.sum.HeldSince = &t
		p.sum.NextAttemptAt = nil
		p.sum.Message = fmt.Sprintf("%s; held for a person until %s, then its files are removed",
			message, until.Format(time.RFC3339))
		p.dec.Verdict = lifecycle.ImportHeld
		p.at(until)
	}
}

func (p *planner) retry(after time.Duration, message, note string) {
	next := p.now.Add(after)
	t := metav1.NewTime(next)
	p.sum.Phase = catalogv1alpha1.ImportPhasePending
	p.sum.NextAttemptAt = &t
	p.sum.Message = fmt.Sprintf("%s; %s at %s (attempt %d of %d)", message, note, next.Format(time.RFC3339),
		p.sum.Attempts+1, len(RetryLadder)+1)
	p.dec.Verdict = lifecycle.ImportRetry
	p.at(next)
}

// planResult is planFrom's answer: an approved plan (ok), or the class and
// message of an import that imports nothing. note explains the files an
// approved plan leaves behind.
type planResult struct {
	ok      bool
	plan    schema.ImportPlan
	class   commonv1.ImportRejectionClass
	message string
	note    string
}

func refused(rs []Rejection, none string) planResult {
	if b, ok := blocked(rs); ok {
		return planResult{class: commonv1.ImportClassNeedsPerson, message: b.Text}
	}
	return planResult{class: Classify(rs), message: detail(outcomeMessage(rs, none), rs)}
}

// mode is how the execute places files: moved when the engine lets go of
// them (usenet, a finished torrent the client no longer seeds), else
// hard-linked.
func (p *planner) mode() string {
	if r := p.in.Transfer; r != nil && r.CanMoveFiles {
		return schema.ImportModeMove
	}
	return schema.ImportModeHardlink
}

// planFrom applies P53-P66 to an inspection.
func (p *planner) planFrom(insp *schema.ImportInspection) planResult {
	if insp == nil {
		return planResult{class: commonv1.ImportClassTransient, message: "the inspection is missing"}
	}
	if len(insp.Rejections) > 0 {
		rs := make([]Rejection, 0, len(insp.Rejections))
		for _, r := range insp.Rejections {
			rs = append(rs, FromInspect(r))
		}
		return refused(rs, "the payload could not be inspected")
	}
	if p.donor() {
		return p.planDonor(insp)
	}
	if p.in.Profile == nil {
		return planResult{class: commonv1.ImportClassNeedsPerson, message: fmt.Sprintf(
			"quality profile %q cannot be resolved: %s", p.in.ProfileRef, p.in.ProfileError)}
	}
	prof := *p.in.Profile
	manual := p.manual()
	e := &p.in.Entry
	root := ""
	if r := p.in.Transfer; r != nil {
		root = r.ContentRoot
	}

	var (
		rs    []Rejection
		cands []candidate
		kind  commonv1.MediaKind
	)
	for _, f := range insp.Files {
		if len(f.Rejections) > 0 {
			for _, r := range f.Rejections {
				rs = append(rs, FromInspect(r))
			}
			continue
		}
		if f.Proposed == nil {
			rs = append(rs, needsPersonRejection("%s: not attributable to an item", relPath(root, f.Path)))
			continue
		}
		c := candidate{f: f, kind: MediaKindOf(f.Proposed.Kind), keys: keysOf(f)}
		c.rank = rankOf(prof, f)
		kind = c.kind
		cands = append(cands, c)
	}
	// A suspected sample decides nothing once real media is beside it.
	if len(cands) > 0 {
		for i := range rs {
			if rs[i].Sample {
				rs[i].Class = ""
			}
		}
	}
	if !multiFileKind(kind) {
		sortCandidates(cands)
	}

	var (
		moves    []schema.ImportMove
		replaces []schema.MediaFileBasis
		replaced = map[string]bool{}
		dests    = map[string]string{}
		filled   = map[string]string{}
		existing []catalogv1alpha1.MediaFile
	)
	for _, c := range cands {
		f := c.f
		rel := relPath(root, f.Path)
		q := f.Frozen.Quality
		if late := p.filledBy(c, filled); late != "" {
			rs = append(rs, c.lateRejection(rel, string(c.kind)+" "+c.keys[0], late))
			continue
		}
		switch {
		case q != nil && !prof.Allowed(*q):
			rs = append(rs, notAllowedRejection(rel, *q))
			continue
		case q == nil && nonVideoKind(c.kind) && !manual:
			rs = append(rs, needsPersonRejection("%s: the quality of this %s file cannot be determined; only a manual import accepts it",
				rel, c.kind))
			continue
		}
		have := p.existingFor(c)
		compared := comparedFiles(have, e)
		if r, ok := p.gate(rel, c, compared, manual); ok {
			rs = append(rs, r)
			continue
		}
		if !contained(f.Dest, insp.RootFolder) {
			rs = append(rs, blockedRejection("refused to place %s at %s, which is not inside root folder path %q",
				rel, f.Dest, insp.RootFolder))
			continue
		}
		if other, dup := dests[f.Dest]; dup {
			rs = append(rs, incidentalRejection("%s: resolves to the same library path as %s, which this import already places", rel, other))
			continue
		}
		dests[f.Dest] = rel
		name := MediaFileName(f.Proposed.Name, f.Dest)
		moves = append(moves, schema.ImportMove{
			Source: f.Path, Dest: f.Dest, Target: *f.Proposed, Keys: c.keys, MediaFileName: name, Frozen: f.Frozen,
		})
		// The covered items' previous files go (P59, P61): every file of a
		// movie or an episode, a single-file item's current one; a
		// multi-file item's are replaced as a set below.
		var olds []catalogv1alpha1.MediaFile
		switch {
		case multiFileKind(c.kind):
			existing = appendUnique(existing, have)
		case nonVideoKind(c.kind):
			if len(compared) > 0 {
				olds = compared[:1]
			}
		default:
			olds = have
		}
		for i := range olds {
			old := &olds[i]
			if old.Spec.Path == f.Dest || old.Name == name || replaced[old.Name] {
				continue
			}
			replaced[old.Name] = true
			replaces = append(replaces, basisOf(old))
		}
		if !multiFileKind(c.kind) {
			for _, k := range c.keys {
				filled[k] = rel
			}
		}
	}
	if b, ok := blocked(rs); ok {
		return planResult{class: commonv1.ImportClassNeedsPerson, message: b.Text}
	}
	if len(moves) == 0 {
		none := "no importable files found"
		if kind != "" {
			none = fmt.Sprintf("no %s files found", kind)
		}
		return refused(rs, none)
	}
	// A manual import of a multi-file item replaces the item's files all
	// or nothing (P65).
	if manual && multiFileKind(kind) {
		var old []catalogv1alpha1.MediaFile
		for _, mf := range existing {
			if _, rewritten := dests[mf.Spec.Path]; !rewritten {
				old = append(old, mf)
			}
		}
		if len(old) > 0 && len(rs) > 0 {
			rs = append(rs, incidentalRejection("kept the item's %d earlier file(s), because %d file(s) of this release "+
				"are not imported, so it does not wholly replace them", len(old), len(rs)))
		} else {
			for i := range old {
				replaces = append(replaces, basisOf(&old[i]))
			}
		}
	}
	plan := schema.ImportPlan{
		Mode: p.mode(), Moves: moves, Replaces: replaces,
		RootFolder: insp.RootFolder, RecycleBin: insp.RecycleBin, MinFreeBytes: insp.MinFreeBytes,
	}
	note := ""
	if len(rs) > 0 {
		note = detail(fmt.Sprintf("importing %d file(s); left behind", len(moves)), rs)
	}
	return planResult{ok: true, plan: plan, note: note}
}

// filledBy is the file that already filled one of c's items, "" for none.
func (p *planner) filledBy(c candidate, filled map[string]string) string {
	if multiFileKind(c.kind) {
		return ""
	}
	for _, k := range c.keys {
		if by, ok := filled[k]; ok {
			return by
		}
	}
	return ""
}

// gate applies the transcoded (P58), multi-file (P64) and upgrade (P59)
// rules against the files the candidate would replace.
func (p *planner) gate(rel string, c candidate, compared []catalogv1alpha1.MediaFile, manual bool) (Rejection, bool) {
	e := &p.in.Entry
	prof := *p.in.Profile
	for i := range compared {
		if r := transcodedRejection(rel, &compared[i], e, manual); !r.none() {
			return r, true
		}
	}
	if manual {
		return Rejection{}, false
	}
	if multiFileKind(c.kind) {
		if len(compared) > 0 {
			return needsPersonRejection("%s: %s %s already has %d file(s); adding to or replacing a multi-file item "+
				"needs a manual import (%s)", rel, c.kind, c.keys[0], len(compared), overrideHint), true
		}
		return Rejection{}, false
	}
	var q commonv1.Quality
	if c.f.Frozen.Quality != nil {
		q = *c.f.Frozen.Quality
	}
	cand := quality.Candidate{Quality: q}
	if c.f.Frozen.Revision != nil {
		cand.Revision = *c.f.Frozen.Revision
	}
	if c.f.Frozen.FormatScore != nil {
		cand.FormatScore = int(*c.f.Frozen.FormatScore)
	}
	olds := compared
	if nonVideoKind(c.kind) && len(olds) > 1 {
		olds = olds[:1]
	}
	for i := range olds {
		mf := &olds[i]
		current := quality.Candidate{Quality: mf.Spec.Quality, Revision: mf.Spec.Revision}
		if !nonVideoKind(c.kind) {
			current.FormatScore = int(mf.Spec.FormatScore)
		}
		verdict := prof.UpgradeDecision(current, cand)
		if verdict == quality.Upgrade || (!nonVideoKind(c.kind) && replacesWrongLanguage(prof, p.in.OriginalLanguage, mf, c.f.Probe)) {
			continue
		}
		text := fmt.Sprintf("%s: %s", rel, verdictMessage(verdict))
		if len(olds) > 1 || c.kind == commonv1.MediaKindEpisode {
			text = fmt.Sprintf("%s (%s)", text, mf.Spec.MediaRef.Name)
		}
		return verdictRejection(prof, e, q, text), true
	}
	return Rejection{}, false
}

// existingFor is every MediaFile backing any of c's items, each once.
func (p *planner) existingFor(c candidate) []catalogv1alpha1.MediaFile {
	var out []catalogv1alpha1.MediaFile
	for _, k := range c.keys {
		out = appendUnique(out, p.in.Existing[ItemKey(c.kind, k)])
	}
	return out
}

// ItemKey is the Existing key of one item: mfindex.ItemKey's value,
// "<kind>/<name>".
func ItemKey(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

func appendUnique(out, add []catalogv1alpha1.MediaFile) []catalogv1alpha1.MediaFile {
	for i := range add {
		dup := false
		for j := range out {
			if out[j].Name == add[i].Name {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, add[i])
		}
	}
	return out
}

func basisOf(mf *catalogv1alpha1.MediaFile) schema.MediaFileBasis {
	return schema.MediaFileBasis{Name: mf.Name, UID: string(mf.UID), ResourceVersion: mf.ResourceVersion, Path: mf.Spec.Path}
}

// planDonor checks an audio donor: it must carry the item's missing
// languages and its original, the anchor a graft aligns on.
func (p *planner) planDonor(insp *schema.ImportInspection) planResult {
	d := p.in.Donor
	if d == nil {
		return planResult{class: commonv1.ImportClassItemState, message: "a donor is for an episode or a movie, and its item is gone"}
	}
	if len(d.Missing) == 0 {
		return planResult{class: commonv1.ImportClassItemState, message: fmt.Sprintf(
			"%s %q no longer lacks a language a donor could graft", d.Item.Kind, d.Item.Name)}
	}
	anchor, ok := lang.Normalize(d.Original)
	if !ok {
		return planResult{class: commonv1.ImportClassNeedsPerson, message: fmt.Sprintf(
			"%s %q has no known original language to align a donor on", d.Item.Kind, d.Item.Name)}
	}
	if len(insp.Files) == 0 {
		return planResult{class: commonv1.ImportClassReleaseFault, message: MessageEveryFileRejected + ": no video or audio file in the download"}
	}
	f := insp.Files[0]
	root := ""
	if r := p.in.Transfer; r != nil {
		root = r.ContentRoot
	}
	rel := relPath(root, f.Path)
	if f.Probe == nil {
		return planResult{class: commonv1.ImportClassTransient, message: rel + ": the donor's file could not be probed"}
	}
	have := map[string]bool{}
	for _, l := range f.Probe.AudioLanguages {
		have[baseTag(l)] = true
	}
	var lacks []string
	for _, l := range append([]string{string(anchor)}, d.Missing...) {
		if !have[baseTag(l)] {
			lacks = append(lacks, l)
		}
	}
	if len(lacks) > 0 {
		return planResult{class: commonv1.ImportClassReleaseFault, message: fmt.Sprintf(
			"%s: %s: no tagged %v audio track", MessageEveryFileRejected, rel, lacks)}
	}
	if insp.RootFolder == "" {
		return planResult{class: commonv1.ImportClassNeedsPerson, message: "the donor's item names no root folder"}
	}
	dir, dest := donorDest(insp.RootFolder, d.Item, f.Path)
	return planResult{ok: true, plan: schema.ImportPlan{
		Mode: p.mode(), RootFolder: insp.RootFolder, RecycleBin: insp.RecycleBin, MinFreeBytes: insp.MinFreeBytes,
		Donor: &schema.DonorPlan{Source: f.Path, Dir: dir, Dest: dest},
	}}
}
