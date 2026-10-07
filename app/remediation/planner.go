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

package remediation

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// PlannerName names one remediation.
type PlannerName string

// The planners, by name.
const (
	PlannerAdopt     PlannerName = "adopt" // release N only (§7)
	PlannerProbe     PlannerName = "probe"
	PlannerTranscode PlannerName = "transcode"
	PlannerGraft     PlannerName = "graft"
	PlannerNaming    PlannerName = "naming"
	PlannerSubtitles PlannerName = "subtitles"
	PlannerMarkers   PlannerName = "markers"
)

// Order is the order planners run in within one pass: the probe first (every
// later planner reads its draft), graft after transcode (the join is decided
// at dispatch), naming after both (it holds a rename while either is in
// flight), subtitles after naming (sidecars follow the stem), markers last
// (it gates on the draft's probe hash). TestPlannerOrder holds it; release N
// prepends PlannerAdopt (§7.3.3).
var Order = []PlannerName{PlannerProbe, PlannerTranscode, PlannerGraft, PlannerNaming, PlannerSubtitles, PlannerMarkers}

// Planner is one remediation. Gather reads, Plan decides, Copy names its
// fields (§3.5). Plan is pure: no I/O, no clock but v.Now, no randomness.
type Planner[In any] interface {
	Name() PlannerName
	Applies(mf *catalogv1alpha1.MediaFile) bool
	Gather(ctx context.Context, env *Env, v *View) (In, error)
	Plan(v *View, in In, out *catalogv1alpha1.MediaFileStatus) (Result, error)
	// Copy copies exactly this planner's fields -- its block, flat fields,
	// nonce leaves and condition types -- from one status into another.
	Copy(from, into *catalogv1alpha1.MediaFileStatus)
}

// MainIntent is the spec takeover the probe planner's incorporation hands
// the main-resource apply (§3.10).
type MainIntent struct {
	Path      string
	SizeBytes int64
	ModTime   metav1.Time
	Swap      bool // a transcode swap: spec.original goes false
}

// Event is one Kubernetes Event on the MediaFile, for a transition from
// Prev to the applied status only. Action defaults to the planner's name.
type Event struct {
	Type, Reason, Message, Action string
}

// RecordNote is one record outcome a pass incorporated or timed out, counted
// into clustarr_record_incorporations_total / clustarr_record_timeouts_total
// once the apply lands (D-F3-4).
type RecordNote struct {
	State    string
	TimedOut bool
}

// Result is what a planner's Plan decided beyond its fields.
type Result struct {
	Main    *MainIntent  // probe only: the spec takeover of §3.10
	Effects []Effect     // run after the status apply lands (§3.8)
	Due     time.Time    // when this planner next needs a pass; zero is no timer
	Again   bool         // an earlier planner's input changed this pass: requeue in 1 s
	Events  []Event      // Kubernetes Events, for transitions Prev -> out only
	Unpaced bool         // the change is exempt from the status-write limiter (§3.7)
	Records []RecordNote // record outcomes, counted after the apply
}

// Bound is a Planner with its input type erased, so Order holds every
// planner in one slice.
type Bound interface {
	Name() PlannerName
	Applies(mf *catalogv1alpha1.MediaFile) bool
	Copy(from, into *catalogv1alpha1.MediaFileStatus)
	gather(ctx context.Context, env *Env, v *View) (any, error)
	plan(v *View, in any, out *catalogv1alpha1.MediaFileStatus) (Result, error)
	unwrap() any
}

// Bind erases p's input type.
func Bind[In any](p Planner[In]) Bound { return bound[In]{p: p} }

type bound[In any] struct{ p Planner[In] }

func (b bound[In]) Name() PlannerName                                { return b.p.Name() }
func (b bound[In]) Applies(mf *catalogv1alpha1.MediaFile) bool       { return b.p.Applies(mf) }
func (b bound[In]) Copy(from, into *catalogv1alpha1.MediaFileStatus) { b.p.Copy(from, into) }
func (b bound[In]) unwrap() any                                      { return b.p }

func (b bound[In]) gather(ctx context.Context, env *Env, v *View) (any, error) {
	return b.p.Gather(ctx, env, v)
}

func (b bound[In]) plan(v *View, in any, out *catalogv1alpha1.MediaFileStatus) (Result, error) {
	typed, _ := in.(In) // zero when Gather was skipped
	return b.p.Plan(v, typed, out)
}

// CopyConditions replaces, in into, each condition of types with from's (or
// removes it when from has none): the condition half of a planner's Copy.
func CopyConditions(from, into *catalogv1alpha1.MediaFileStatus, types ...string) {
	for _, typ := range types {
		k8s.RemoveCondition(&into.Conditions, typ)
		if c := k8s.FindCondition(from.Conditions, typ); c != nil {
			into.Conditions = append(into.Conditions, *c.DeepCopy())
		}
	}
}

// gatherTimeout is each planner's Gather deadline (§3.4 step 3).
func gatherTimeout(n PlannerName) time.Duration {
	switch n {
	case PlannerProbe:
		return 15 * time.Second
	case PlannerSubtitles:
		return 30 * time.Second
	}
	return 10 * time.Second
}

// conditionFor is n's <Planner>PlannerError condition type (§2.8).
func conditionFor(n PlannerName) string {
	switch n {
	case PlannerProbe:
		return catalogv1alpha1.MediaFileConditionProbePlannerError
	case PlannerNaming:
		return catalogv1alpha1.MediaFileConditionNamingPlannerError
	case PlannerTranscode:
		return catalogv1alpha1.MediaFileConditionTranscodePlannerError
	case PlannerGraft:
		return catalogv1alpha1.MediaFileConditionGraftPlannerError
	case PlannerSubtitles:
		return catalogv1alpha1.MediaFileConditionSubtitlesPlannerError
	case PlannerMarkers:
		return catalogv1alpha1.MediaFileConditionMarkersPlannerError
	}
	return ""
}
