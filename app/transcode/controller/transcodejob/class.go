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
	"context"
	"errors"
	"sort"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// gpuClasses is admission.GPUClasses.
var gpuClasses = admission.GPUClasses

// ChooseClass is admission.ChooseClass.
func ChooseClass(gpuNodes, unschedulable map[transcodev1alpha1.Hardware]bool,
	free map[transcodev1alpha1.Hardware]int32, fallback, gpuOnly bool,
) transcodev1alpha1.Hardware {
	return admission.ChooseClass(gpuNodes, unschedulable, free, fallback, gpuOnly)
}

// hardwareFor is the hardware tj asks for under its profile tp: its own
// spec.hardware when set, else the profile's, where empty is the CRD
// default, auto. A nil tp (a deleted profile) asks for nothing.
func hardwareFor(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) transcodev1alpha1.Hardware {
	if tj.Spec.Hardware != nil && *tj.Spec.Hardware != "" {
		return *tj.Spec.Hardware
	}
	if tp == nil {
		return ""
	}
	if tp.Spec.Hardware == "" {
		return transcodev1alpha1.HardwareAuto
	}
	return tp.Spec.Hardware
}

// choosesClassFor reports whether tj chooses its class per dispatch under
// its profile tp: auto or gpu, from hardwareFor. A deleted profile pins
// nothing that can be asked about, so it does not.
func choosesClassFor(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) bool {
	hw := hardwareFor(tj, tp)
	return hw == transcodev1alpha1.HardwareAuto || hw == transcodev1alpha1.HardwareGPU
}

// gpuOnlyFor reports whether tj waits for a GPU slot rather than take a cpu
// one: hardware gpu.
func gpuOnlyFor(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) bool {
	return hardwareFor(tj, tp) == transcodev1alpha1.HardwareGPU
}

// waitingForGPU is the message a gpu job admission passes over carries.
const waitingForGPU = admission.WaitingForGPU

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
		if choosesClassFor(c.tj, c.tp) && encodesVideo(c.tj.Status.Plan) {
			class = ChooseClass(gpu, r.unschedulableFor(c.tp), free, c.tj.Status.FallbackReason != "", gpuOnlyFor(c.tj, c.tp))
		}
		if class == "" {
			holds = append(holds, heldJob{tj: c.tj, msg: waitingForGPU})
			continue
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

// unhealthyClasses are the GPU classes every fresh report of which says the
// device cannot be used (spec §4; task.ReadEncoderHealth), each with the
// message a job held for it carries. A class with no fresh report is not
// one -- its pool may not exist yet, or its pods are gone -- so it stays
// eligible, and a pod that measures it again reports afresh.
func (r *Reconciler) unhealthyClasses(ctx context.Context) map[transcodev1alpha1.Hardware]string {
	if r.Bus == nil {
		return nil
	}
	kv := r.Bus.KV(events.BucketProgress)
	var out map[transcodev1alpha1.Hardware]string
	for _, class := range gpuClasses {
		health, err := task.ReadEncoderHealth(ctx, kv, string(class), r.now().Time)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcode: cannot read the devices' health", "class", class, "error", err)
			continue
		}
		if msg, ok := unhealthyMessage(class, health); ok {
			if out == nil {
				out = map[transcodev1alpha1.Hardware]string{}
			}
			out[class] = msg
		}
	}
	return out
}

// unhealthyMessage is admission.UnhealthyMessage.
func unhealthyMessage(class transcodev1alpha1.Hardware, health map[string]task.NodeHealth) (string, bool) {
	return admission.UnhealthyMessage(class, health)
}

// rerouteUnhealthy takes every auto job Queued on a GPU class reported
// unusable (unhealthyClasses) back to Planned with a fallbackReason naming
// the reason, which keeps it on cpu from then on: the class's pods pull
// nothing while unhealthy, and a pool with work dispatched never suspends,
// so the job would otherwise wait on it for good. A pinned job waits for
// its class, as rerouteUnschedulable leaves it.
func (r *Reconciler) rerouteUnhealthy(ctx context.Context, profiles map[string]*transcodev1alpha1.TranscodeProfile,
	tjs []transcodev1alpha1.TranscodeJob, unhealthy map[transcodev1alpha1.Hardware]string,
) (int, error) {
	if len(unhealthy) == 0 {
		return 0, nil
	}
	var (
		errs     []error
		rerouted int
	)
	for i := range tjs {
		tj := &tjs[i]
		msg, ok := unhealthy[tj.Status.Hardware]
		tp := profiles[tj.Spec.ProfileRef]
		if !ok || tp == nil || tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseQueued || !choosesClassFor(tj, tp) ||
			k8s.IsDeleting(tj) || (tj.Spec.Suspend != nil && *tj.Spec.Suspend) {
			continue
		}
		done, err := r.reroute(ctx, tj, msg, gpuOnlyFor(tj, tp))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if done {
			rerouted++
			logging.FromContext(ctx).InfoContext(ctx, "transcode: took a queued auto job back from a GPU class reported unusable",
				"transcodeJob", tj.Namespace+"/"+tj.Name, "class", tj.Status.Hardware)
		}
	}
	return rerouted, errors.Join(errs...)
}
