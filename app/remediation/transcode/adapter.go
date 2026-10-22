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

package transcode

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/records"
)

// Options configures the transcode planner.
type Options struct {
	// Bus carries the loop's sources.
	Bus events.Bus
	// Ledger is the admission ledger (*admission.Ledger), through
	// transcodeplan.Admission; nil grants nothing.
	Ledger transcodeplan.Admission
	// LoopSource is S22, admission's wake of the loop's queue
	// (admission.LoopSource); nil adds none.
	LoopSource source.TypedSource[remediation.Key]
	// Records is clustarr-transcodes, the loop's half (task.Requests).
	Records *records.Requester[*task.Record]
	// Leases is clustarr-transcode-leases: a Running dispatch's lease is
	// read for the lost-worker row (§5.9).
	Leases events.KV
	// Admin answers whether a file's task is still stored (§5.9).
	Admin events.StreamAdmin
	// Withdrawal takes a dispatch back (§5.10): the record, the lease's
	// cancel marker, the purge.
	Withdrawal task.Withdrawal
}

// Adapter is the transcode planner.
type Adapter struct{ o Options }

// New is the transcode planner over o.
func New(o Options) *Adapter { return &Adapter{o: o} }

// In is what Gather read.
type In struct {
	Profiles   []transcodev1alpha1.TranscodeProfile
	ItemExists bool
	// ItemExcluded is whether the item, or an episode's series, carries
	// transcode.clustarr.io/exclude (readItem).
	ItemExcluded bool
	// OriginalLanguage is the item's original language (readItem).
	OriginalLanguage string
	// UpgradeDownloading is whether the item has a video grab in flight
	// (readItem, transcodeplan.UpgradeDownloading).
	UpgradeDownloading bool
	// Join is what graftplan.Join reads: gathered on a dispatch grant and
	// for a Queued block that carries a join (its owed republish).
	Join            graftplan.JoinInput
	JoinRead        bool
	Ledger          transcodeplan.Ledger
	Took            bool // Gather took a grant (TakeGrant): Plan confirms or releases it
	Read            bool // Gather ran
	Record          *task.Record
	RecordRev       uint64
	Folders         []catalogv1alpha1.RootFolder
	Lease           transcodeplan.LeaseView
	TaskStored      *bool
	Intents         transcodeplan.Intents
	DeadLettered    string
	SpecPathMissing bool
	OutputExists    bool
	OutputSize      int64
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerTranscode }

// Applies: only movie and episode files are transcoded.
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	return transcodeplan.EligibleKind(mf.Spec.MediaRef.Kind)
}

// Plan is transcodeplan.Plan over the draft. It runs only while the draft's
// probe is current: the loop's structural gate keeps this planner's last
// block otherwise (§3.5).
func (a *Adapter) Plan(v *remediation.View, in In, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	if !in.Read {
		return remediation.Result{}, nil
	}
	f := transcodeplan.File{
		MF: v.File, Labels: v.Labels(), Status: v.Draft, ProbeCurrent: v.ProbeCurrent(),
		ItemExists: in.ItemExists, ItemExcluded: in.ItemExcluded, StandaloneGraft: graftplan.Standalone(v.Prev.Graft),
		UpgradeDownloading: in.UpgradeDownloading,
	}
	if p, ok := transcodeplan.ParsePriority(in.Intents.Priority); ok {
		f.Priority = &p
	}
	var nonces catalogv1alpha1.HandledNonces
	if v.Prev.HandledNonces != nil {
		nonces = *v.Prev.HandledNonces
	}
	e := transcodeplan.Eligible(f, in.Profiles)
	profiles := make(map[string]*transcodev1alpha1.TranscodeProfile, len(in.Profiles))
	for i := range in.Profiles {
		profiles[in.Profiles[i].Name] = &in.Profiles[i]
	}
	d := transcodeplan.Plan(transcodeplan.Inputs{
		File: v.File, OriginalLanguage: in.OriginalLanguage, Prev: v.Prev.Transcode, Draft: v.Draft, ProbeCurrent: f.ProbeCurrent,
		Eligibility: e, Ledger: in.Ledger, Now: v.Now,
		Record: in.Record, RecordRev: in.RecordRev, Folders: in.Folders, Issue: v.Issue,
		Lease: in.Lease, TaskStored: in.TaskStored, Profiles: profiles,
		Intents: in.Intents, Nonces: nonces, DeadLettered: in.DeadLettered,
		SpecPathMissing: in.SpecPathMissing, OutputExists: in.OutputExists, OutputSize: in.OutputSize,
		PrevGraft: v.Prev.Graft, Joinable: a.joinable(v, in),
	})
	out.Transcode = d.Block
	if d.Nonces != nil {
		setNonces(out, d.Nonces.TranscodeRetry, d.Nonces.TranscodeCancel)
	}
	res := remediation.Result{
		Due: d.Due, Again: d.Again, Events: kubeEvents(d.Events), Eligibility: &e,
		// Acting on a held grant, a dispatch, an incorporation and a
		// person's intent are exempt from the status-write limiter (§3.7).
		Unpaced: in.Took || d.Unpaced || d.Nonces != nil,
		Effects: a.effectsOf(v, in, d),
	}
	for _, st := range d.Records {
		res.Records = append(res.Records, remediation.RecordNote{State: st})
	}
	return res, nil
}

// joinable is the graft a dispatch of this pass carries (graftplan.Join
// over the gathered item), nil when Gather read none.
func (*Adapter) joinable(v *remediation.View, in In) *graftplan.Joinable {
	if !in.JoinRead {
		return nil
	}
	return graftplan.Join(graftplan.View{File: v.File, Prev: v.Prev, Draft: v.Draft, Now: v.Now, ProbeCurrent: v.ProbeCurrent()}, in.Join)
}

// Copy is transcode's row of §3.5's table: status.transcode and
// handledNonces.transcodeRetry and transcodeCancel.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Transcode = from.Transcode.DeepCopy()
	var retry, cancel string
	if from.HandledNonces != nil {
		retry, cancel = from.HandledNonces.TranscodeRetry, from.HandledNonces.TranscodeCancel
	}
	setNonces(into, retry, cancel)
}

// setNonces sets the two transcode nonce leaves alone, beside the subtitle
// leaf, on a copy of into's HandledNonces.
func setNonces(st *catalogv1alpha1.MediaFileStatus, retry, cancel string) {
	if st.HandledNonces == nil {
		if retry == "" && cancel == "" {
			return
		}
		st.HandledNonces = &catalogv1alpha1.HandledNonces{}
	} else {
		cp := *st.HandledNonces
		st.HandledNonces = &cp
	}
	st.HandledNonces.TranscodeRetry, st.HandledNonces.TranscodeCancel = retry, cancel
	if *st.HandledNonces == (catalogv1alpha1.HandledNonces{}) {
		st.HandledNonces = nil
	}
}

// kubeEvents renders transcodeplan's Events for the loop, under action
// "transcode".
func kubeEvents(in []transcodeplan.Event) []remediation.Event {
	out := make([]remediation.Event, 0, len(in))
	for _, e := range in {
		out = append(out, remediation.Event{Type: e.Type, Reason: e.Reason, Message: e.Message, Action: "transcode"})
	}
	return out
}

// Withdraw runs a transcode withdrawal (§5.10) through task.Withdrawal --
// the one implementation admission's sweep shares -- and holds its failure
// in the ledger for the next pass's message; a failure is transient, so the
// pass requeues on the per-key backoff.
func (a *Adapter) Withdraw(ctx context.Context, _ *remediation.Env, w remediation.Withdraw) error {
	err := a.o.Withdrawal.Withdraw(ctx, w.UID, w.Seq)
	if a.o.Ledger != nil {
		a.o.Ledger.WithdrawFailed(types.UID(w.UID), err)
	}
	if err != nil {
		return remediation.Transient(err)
	}
	return nil
}
