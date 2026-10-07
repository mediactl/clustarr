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
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/types"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// GPUClasses are the GPU classes an auto file may be sent to, in the
// priority order spec §18.5 gives them: nvidia, then intel. cpu is what is
// left.
var GPUClasses = []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel}

// ChooseClass picks the class of a file that chooses one per dispatch (spec
// §18.5): the first GPU class, in priority order, with a labelled GPU node,
// a free slot and a schedulable pool; else cpu. A recorded fallback reason
// pins cpu.
//
// gpuOnly is hardware: gpu (2026-10-01): when a GPU class could take the
// file but none has a free slot, the answer is "" -- wait -- rather than
// cpu. It is still cpu when no GPU class is usable at all (no labelled
// node, or every pool unschedulable), since the file would otherwise wait
// for good.
//
// free counts this pass's own dispatches: the caller takes a slot from it
// for each file it expects admission to dispatch, so the next file sees
// what is left.
func ChooseClass(gpuNodes, unschedulable map[transcodev1alpha1.Hardware]bool,
	free map[transcodev1alpha1.Hardware]int32, fallback, gpuOnly bool,
) transcodev1alpha1.Hardware {
	if fallback {
		return transcodev1alpha1.HardwareCPU
	}
	usable := false
	for _, c := range GPUClasses {
		if !gpuNodes[c] || unschedulable[c] {
			continue
		}
		usable = true
		if free[c] > 0 {
			return c
		}
	}
	if gpuOnly && usable {
		return ""
	}
	return transcodev1alpha1.HardwareCPU
}

// UnhealthyMessage reports whether every node in health reported class's
// device unusable, with a message naming the first such node (by name) and
// its reason. No report is not unhealthy.
func UnhealthyMessage(class transcodev1alpha1.Hardware, health map[string]task.NodeHealth) (string, bool) {
	if len(health) == 0 {
		return "", false
	}
	nodes := make([]string, 0, len(health))
	for n, h := range health {
		if h.Healthy {
			return "", false
		}
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return fmt.Sprintf("waiting for a usable %s device: every node that measured one reported it unusable (%s: %s)",
		class, nodes[0], health[nodes[0]].Error), true
}

// WaitingForGPU is the message a gpu file admission passes over carries.
const WaitingForGPU = "waiting for a free GPU slot (hardware: gpu)"

// hardwareOf is v's hardware in force, empty read as auto.
func hardwareOf(v FileView) transcodev1alpha1.Hardware {
	if v.Hardware == "" {
		return transcodev1alpha1.HardwareAuto
	}
	return v.Hardware
}

// choosesClass reports whether v chooses its class per dispatch: auto or
// gpu.
func choosesClass(v FileView) bool {
	hw := hardwareOf(v)
	return hw == transcodev1alpha1.HardwareAuto || hw == transcodev1alpha1.HardwareGPU
}

// gpuOnly reports whether v waits for a GPU slot rather than take a cpu
// one: hardware gpu.
func gpuOnly(v FileView) bool { return hardwareOf(v) == transcodev1alpha1.HardwareGPU }

// classFor is the class a Planned file dispatches to when it does not
// choose one per dispatch -- a pinned file, or an auto one whose plan
// encodes nothing: a GPU pin goes to its class whatever its plan (a remux
// copies the video, so the GPU pool runs it); otherwise the plan's
// encoder's class, and cpu once a fallback reason is recorded or with no
// plan (spec §18.5).
func classFor(v FileView) transcodev1alpha1.Hardware {
	if hw := hardwareOf(v); hw == transcodev1alpha1.HardwareNVIDIA || hw == transcodev1alpha1.HardwareIntel {
		return hw
	}
	if v.Fallback || v.Encoder == "" {
		return transcodev1alpha1.HardwareCPU
	}
	return transcodeplan.HardwareForEncoder(v.Encoder)
}

// candidate is one Planned file competing in an admission pass, with the
// Slot it competes as; slot.Hardware is empty until assignClasses fills it.
type candidate struct {
	uid  types.UID
	v    FileView
	slot Slot
}

// hold is a candidate admission passes over this pass, and why.
type hold struct {
	uid        types.UID
	msg, cause string
}

// assignClasses gives each candidate the class it competes for in this
// pass, then holds out every one whose (profile, class) pool is held
// (ruling R4: the class first, so an auto file is held for exactly the pool
// it would use).
//
// Candidates are taken in [Admit]'s own order (AdmitsBefore). A pinned
// file, or one whose plan encodes nothing, keeps classFor's class; an auto
// file that encodes gets [ChooseClass]'s, from gpu (the classes with a GPU
// node), the pools of its profile marked unschedulable, and the slots still
// free -- the budget, less the running files, less the candidates before it
// that Admit will take. Admit stays the enforcement: this only predicts it.
func assignClasses(cands []candidate, running []Slot, gpu map[transcodev1alpha1.Hardware]bool,
	unschedulable func(profile string) map[transcodev1alpha1.Hardware]bool, held map[pool.Key]string,
	profileUIDs map[string]types.UID, limits, slots map[string]int32,
) (queued []candidate, holds []hold) {
	free := make(map[transcodev1alpha1.Hardware]int32, len(slots))
	for class, n := range slots {
		free[transcodev1alpha1.Hardware(class)] = n
	}
	perProfile := map[string]int32{}
	for _, s := range running {
		free[transcodev1alpha1.Hardware(s.Hardware)]--
		perProfile[s.Profile]++
	}
	ordered := make([]candidate, len(cands))
	copy(ordered, cands)
	sort.SliceStable(ordered, func(i, j int) bool { return AdmitsBefore(ordered[i].slot, ordered[j].slot) })
	for _, c := range ordered {
		class := classFor(c.v)
		if choosesClass(c.v) && c.v.Encodes {
			class = ChooseClass(gpu, unschedulable(c.v.Profile), free, c.v.Fallback, gpuOnly(c.v))
		}
		if class == "" {
			holds = append(holds, hold{uid: c.uid, msg: WaitingForGPU, cause: transcodeplan.ReasonWaitingForGPU})
			continue
		}
		k := pool.Key{Profile: c.v.Profile, ProfileUID: profileUIDs[c.v.Profile], Class: class}
		if msg, ok := held[k]; ok {
			holds = append(holds, hold{uid: c.uid, msg: msg, cause: transcodeplan.ReasonPoolDraining})
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
