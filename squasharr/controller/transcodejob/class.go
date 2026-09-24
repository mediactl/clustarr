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

package transcodejob

import (
	"sort"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
)

// gpuClasses are the GPU classes an auto job may be sent to, in the priority
// order spec §18.5 gives them: nvidia, then intel. cpu is what is left.
var gpuClasses = []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel}

// ChooseClass picks an auto job's class (spec §18.5): the first GPU class, in
// priority order, with a labelled GPU node, a free slot and a schedulable
// pool; else cpu. A recorded fallback reason pins cpu.
//
// free counts this pass's own dispatches: the caller takes a slot from it
// for each job it expects admission to dispatch, so the next job sees what
// is left.
func ChooseClass(gpuNodes, unschedulable map[transcodev1alpha1.Hardware]bool,
	free map[transcodev1alpha1.Hardware]int32, fallback bool,
) transcodev1alpha1.Hardware {
	if !fallback {
		for _, c := range gpuClasses {
			if gpuNodes[c] && free[c] > 0 && !unschedulable[c] {
				return c
			}
		}
	}
	return transcodev1alpha1.HardwareCPU
}

// isAutoFor reports whether tj chooses its class per dispatch under its
// profile tp: its own spec.hardware when set, else the profile's, where empty
// is the CRD default, auto. A nil tp (a deleted profile) pins nothing that
// can be asked about, so it is not auto.
func isAutoFor(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) bool {
	if tj.Spec.Hardware != nil && *tj.Spec.Hardware != "" {
		return *tj.Spec.Hardware == transcodev1alpha1.HardwareAuto
	}
	return tp != nil && (tp.Spec.Hardware == transcodev1alpha1.HardwareAuto || tp.Spec.Hardware == "")
}

// encodesVideo reports whether plan p runs a video encoder, which is all a
// GPU is for: a remux copies the video and takes a CPU slot
// (hardwareForEncoder), so only an encoding plan competes for a GPU class.
func encodesVideo(p *transcodev1alpha1.Plan) bool {
	return p != nil && p.Mode == transcodev1alpha1.PlanModeTranscode
}

// candidate is one Planned job competing in an admission pass, with its
// profile and the Slot it competes as. slot.Hardware is empty until
// assignClasses fills it.
type candidate struct {
	tj   *transcodev1alpha1.TranscodeJob
	tp   *transcodev1alpha1.TranscodeProfile
	slot Slot
}

// heldJob is a candidate admission passes over this pass, and why.
type heldJob struct {
	tj  *transcodev1alpha1.TranscodeJob
	msg string
}

// assignClasses gives each candidate the class it competes for in this pass,
// then holds out every one whose (profile, class) pool is held (ruling R4:
// the class first, so an auto job is held for exactly the pool it would use
// -- never for a GPU pool it would not have been sent to, and always for the
// one it would).
//
// Candidates are taken in [Admit]'s own order (admitsBefore). A pinned job,
// or one whose plan encodes nothing, keeps classFor's class; an auto job
// that encodes gets [ChooseClass]'s, from gpu (the classes with a GPU node),
// the pools of its profile marked unschedulable, and the slots still free --
// the budget, less the dispatched jobs, less the candidates before it that
// Admit will take. A candidate takes a slot from free exactly when Admit
// will admit it (a free slot of its class, and its profile under its
// maxConcurrent), so a job Admit passes over for its profile's limit, or one
// held here, leaves its GPU slot to the next. Admit stays the enforcement:
// this only predicts it, so that the classes it is handed are ones it can
// fill.
func (r *Reconciler) assignClasses(cands []candidate, running []Slot, gpu map[transcodev1alpha1.Hardware]bool,
	held map[pool.Key]string, limits map[string]int32,
) (queued []candidate, holds []heldJob) {
	free := make(map[transcodev1alpha1.Hardware]int32, len(r.Slots))
	for class, n := range r.Slots {
		free[transcodev1alpha1.Hardware(class)] = n
	}
	perProfile := map[string]int32{}
	for _, s := range running {
		free[transcodev1alpha1.Hardware(s.Hardware)]--
		perProfile[s.Profile]++
	}

	ordered := make([]candidate, len(cands))
	copy(ordered, cands)
	sort.SliceStable(ordered, func(i, j int) bool { return admitsBefore(ordered[i].slot, ordered[j].slot) })

	for _, c := range ordered {
		class := r.classFor(c.tj, c.tp)
		if isAutoFor(c.tj, c.tp) && encodesVideo(c.tj.Status.Plan) {
			class = ChooseClass(gpu, r.unschedulableFor(c.tp), free, c.tj.Status.FallbackReason != "")
		}
		if msg, ok := held[poolKeyFor(c.tp, class)]; ok {
			holds = append(holds, heldJob{tj: c.tj, msg: msg})
			continue
		}
		c.slot.Hardware = string(class)
		queued = append(queued, c)
		if limit := limits[c.slot.Profile]; free[class] > 0 && (limit <= 0 || perProfile[c.slot.Profile] < limit) {
			free[class]--
			perProfile[c.slot.Profile]++
		}
	}
	return queued, holds
}

// unschedulableFor is the GPU classes whose pool of tp is marked
// unschedulable now (pools.go, rerouteUnschedulable).
func (r *Reconciler) unschedulableFor(tp *transcodev1alpha1.TranscodeProfile) map[transcodev1alpha1.Hardware]bool {
	var out map[transcodev1alpha1.Hardware]bool
	now := r.now().Time
	for _, class := range gpuClasses {
		if until, ok := r.unschedulable[poolKeyFor(tp, class)]; ok && now.Before(until) {
			if out == nil {
				out = map[transcodev1alpha1.Hardware]bool{}
			}
			out[class] = true
		}
	}
	return out
}
