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

package admission

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// FileView is one file as the ledger counts it (§5.5), built from the
// applied status (or a quorum read), never from a stale cache.
type FileView struct {
	UID           types.UID
	Key           types.NamespacedName
	Seq           int64                          // status.lastSeq: orders every observation, an absent block included
	Phase         catalogv1alpha1.TranscodePhase // "" when there is no block
	DispatchSeq   int64
	Class         transcodev1alpha1.Hardware
	Pool          string
	Profile       string
	ProfileUID    types.UID
	PlannedAt     time.Time
	NextAttemptAt time.Time
	Suspended     bool
	Priority      int32
	Hardware      transcodev1alpha1.Hardware
	Encodes       bool   // the plan's mode is transcode
	Encoder       string // classFor's input
	Fallback      bool
	JoinedGraft   bool // transcode.joinedGraft names the in-flight dispatch: no standalone graft beside it
	// The graft block (§5.13): its phase and reason, its standalone Job,
	// and its donor's import time, the graft-slot order.
	GraftPhase      catalogv1alpha1.GraftPhase
	GraftReason     string
	GraftJobName    string
	DonorImportedAt time.Time
	Eligible        bool
	EligibleKnown   bool // false when the transcode planner did not run this pass: keep the last answer
	VerdictHolds    bool // a Skipped or Failed block for the current (tag, probe)
}

// rank orders observations of one seq (§5.5): Queued < Running < the rest.
func rank(p catalogv1alpha1.TranscodePhase) int {
	switch p {
	case catalogv1alpha1.TranscodePhaseQueued:
		return 1
	case catalogv1alpha1.TranscodePhaseRunning:
		return 2
	}
	return 3
}

// windowPhase reports a phase that holds a place in its profile's window.
func windowPhase(p catalogv1alpha1.TranscodePhase) bool {
	switch p {
	case catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned, catalogv1alpha1.TranscodePhaseQueued,
		catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
		return true
	}
	return false
}

// slotPhase reports a phase that holds a slot of its class.
func slotPhase(p catalogv1alpha1.TranscodePhase) bool {
	return p == catalogv1alpha1.TranscodePhaseQueued || p == catalogv1alpha1.TranscodePhaseRunning
}

// FileViewOf renders the view of a file from its status and the transcode
// planner's eligibility this pass (nil: the planner did not run; the ledger
// keeps its last answer). ProfileUID is filled by the ledger from the
// profiles it was last given.
func FileViewOf(mf metav1.Object, st *catalogv1alpha1.MediaFileStatus, e *transcodeplan.Eligibility) FileView {
	v := FileView{UID: mf.GetUID(), Key: types.NamespacedName{Namespace: mf.GetNamespace(), Name: mf.GetName()}, Seq: st.LastSeq}
	if e != nil {
		v.Eligible, v.EligibleKnown, v.Priority = e.Eligible, true, e.Priority
		if e.Profile != nil {
			v.Profile, v.Hardware = e.Profile.Name, e.Profile.Spec.Hardware
		}
	}
	if b := st.Transcode; b != nil {
		v.Phase = b.Phase
		if b.Profile != "" {
			v.Profile = b.Profile
		}
		v.Class, v.Pool, v.Suspended = transcodev1alpha1.Hardware(b.Class), b.Pool, b.Suspended
		if b.Priority != 0 {
			v.Priority = b.Priority
		}
		if b.Hardware != "" {
			v.Hardware = transcodev1alpha1.Hardware(b.Hardware)
		}
		if b.PlannedAt != nil {
			v.PlannedAt = b.PlannedAt.Time
		}
		if b.NextAttemptAt != nil {
			v.NextAttemptAt = b.NextAttemptAt.Time
		}
		if b.Dispatch != nil {
			v.DispatchSeq = b.Dispatch.Seq
		}
		if b.Plan != nil {
			v.Encodes, v.Encoder = b.Plan.Mode == catalogv1alpha1.TranscodeModeTranscode, b.Plan.Encoder
		}
		v.Fallback = b.FallbackReason != ""
		v.JoinedGraft = b.JoinedGraft != nil && b.Dispatch.InFlight()
		v.VerdictHolds = e != nil && (b.Phase == catalogv1alpha1.TranscodePhaseSkipped || b.Phase == catalogv1alpha1.TranscodePhaseFailed) &&
			b.ProfileHash == e.Hash && b.ProbeHash == st.ProbeHash
	}
	if g := st.Graft; g != nil {
		v.GraftPhase, v.GraftReason, v.GraftJobName = g.Phase, g.Reason, g.JobName
		if g.DonorImportedAt != nil {
			v.DonorImportedAt = g.DonorImportedAt.Time
		}
	}
	return v
}
