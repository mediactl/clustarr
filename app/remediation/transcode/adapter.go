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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
)

// Options configures the transcode planner.
type Options struct {
	// Bus carries the loop's sources.
	Bus events.Bus
	// AudioGraftIndex is the AudioGraft index by status.mediaFileRef
	// graftstate.StandaloneFor reads (until F7.2); "" reads none.
	AudioGraftIndex string
}

// Adapter is the transcode planner.
type Adapter struct{ o Options }

// New is the transcode planner over o.
func New(o Options) *Adapter { return &Adapter{o: o} }

// In is what Gather read.
type In struct {
	Profiles        []transcodev1alpha1.TranscodeProfile
	ItemExists      bool
	StandaloneGraft bool
	Ledger          transcodeplan.Ledger
	Read            bool // Gather ran
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
		ItemExists: in.ItemExists, StandaloneGraft: in.StandaloneGraft,
	}
	d := transcodeplan.Plan(transcodeplan.Inputs{
		File: v.File, Prev: v.Prev.Transcode, Draft: v.Draft, ProbeCurrent: f.ProbeCurrent,
		Eligibility: transcodeplan.Eligible(f, in.Profiles), Ledger: in.Ledger, Now: v.Now,
	})
	out.Transcode = d.Block
	return remediation.Result{Due: d.Due, Again: d.Again, Events: kubeEvents(d.Events)}, nil
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
