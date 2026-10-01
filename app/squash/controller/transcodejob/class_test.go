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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestChooseClass(t *testing.T) {
	nv, in, cpu := transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareCPU
	both := map[transcodev1alpha1.Hardware]bool{nv: true, in: true}
	slots := map[transcodev1alpha1.Hardware]int32{nv: 1, in: 1, cpu: 2}
	for _, tc := range []struct {
		name           string
		nodes, unsched map[transcodev1alpha1.Hardware]bool
		free           map[transcodev1alpha1.Hardware]int32
		fallback       bool
		gpuOnly        bool
		want           transcodev1alpha1.Hardware
	}{
		{"nvidia first", both, nil, slots, false, false, nv},
		{"intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, false, in},
		{"cpu when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, false, cpu},
		{"cpu without a GPU node", nil, nil, slots, false, false, cpu},
		{"intel without an nvidia node", map[transcodev1alpha1.Hardware]bool{in: true}, nil, slots, false, false, in},
		{"skip an unschedulable pool", both, map[transcodev1alpha1.Hardware]bool{nv: true}, slots, false, false, in},
		{"a fallback reason pins cpu", both, nil, slots, true, false, cpu},
		{"an over-budget class is full, not free", both, nil, map[transcodev1alpha1.Hardware]int32{nv: -1, in: 0}, false, false, cpu},
		// hardware: gpu (2026-10-01): any GPU, never the CPU for want of a
		// slot -- "" is "wait" -- but the CPU when no GPU class can be used
		// at all, or a GPU already refused the job (its fallback reason).
		{"gpu: nvidia first", both, nil, slots, false, true, nv},
		{"gpu: intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, true, in},
		{"gpu: waits when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, true, ""},
		{"gpu: waits for a full intel when nvidia is unschedulable", both, map[transcodev1alpha1.Hardware]bool{nv: true}, map[transcodev1alpha1.Hardware]int32{nv: 1, cpu: 2}, false, true, ""},
		{"gpu: cpu without a GPU node", nil, nil, slots, false, true, cpu},
		{"gpu: cpu when every GPU pool is unschedulable", both, both, slots, false, true, cpu},
		{"gpu: a fallback reason pins cpu", both, nil, slots, true, true, cpu},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseClass(tc.nodes, tc.unsched, tc.free, tc.fallback, tc.gpuOnly))
		})
	}
}

// choosesClassFor is the jobs that pick their class per dispatch: auto and
// gpu, from the job's own spec.hardware or else its profile's; gpuOnlyFor is
// the ones that never take a CPU slot for want of a GPU one.
func TestChoosesClassForAutoAndGPU(t *testing.T) {
	hw := func(h transcodev1alpha1.Hardware) *transcodev1alpha1.Hardware { return &h }
	tp := func(h transcodev1alpha1.Hardware) *transcodev1alpha1.TranscodeProfile {
		p := &transcodev1alpha1.TranscodeProfile{}
		p.Spec.Hardware = h
		return p
	}
	job := func(h *transcodev1alpha1.Hardware) *transcodev1alpha1.TranscodeJob {
		j := &transcodev1alpha1.TranscodeJob{}
		j.Spec.Hardware = h
		return j
	}
	for _, tc := range []struct {
		name          string
		tj            *transcodev1alpha1.TranscodeJob
		tp            *transcodev1alpha1.TranscodeProfile
		chooses, only bool
	}{
		{"an auto profile", job(nil), tp(transcodev1alpha1.HardwareAuto), true, false},
		{"an empty profile is the CRD default, auto", job(nil), tp(""), true, false},
		{"a gpu profile", job(nil), tp(transcodev1alpha1.HardwareGPU), true, true},
		{"a pinned profile", job(nil), tp(transcodev1alpha1.HardwareNVIDIA), false, false},
		{"a job's own gpu over a pinned profile", job(hw(transcodev1alpha1.HardwareGPU)), tp(transcodev1alpha1.HardwareCPU), true, true},
		{"a job's own pin over a gpu profile", job(hw(transcodev1alpha1.HardwareIntel)), tp(transcodev1alpha1.HardwareGPU), false, false},
		{"a deleted profile", job(nil), nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.chooses, choosesClassFor(tc.tj, tc.tp), "choosesClassFor")
			assert.Equal(t, tc.only, gpuOnlyFor(tc.tj, tc.tp), "gpuOnlyFor")
		})
	}
}

// TestAssignClasses is admission's class assignment (spec §18.5, ruling R4):
// candidates in Admit's order, each auto job given ChooseClass's class
// against the slots the dispatched jobs and the candidates before it leave,
// the hold judged for the pool of the class assigned, and a slot taken only
// by a candidate Admit will admit -- so Admit, handed the classes, admits
// exactly those.
func TestAssignClasses(t *testing.T) {
	nv, cpu := transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareCPU
	profile := func(name string, hw transcodev1alpha1.Hardware) *transcodev1alpha1.TranscodeProfile {
		tp := &transcodev1alpha1.TranscodeProfile{}
		tp.Name, tp.UID, tp.Spec.Hardware = name, types.UID("uid-"+name), hw
		return tp
	}
	auto, pinned := profile("auto", transcodev1alpha1.HardwareAuto), profile("pinned", nv)
	gpuOnly := profile("gpu", transcodev1alpha1.HardwareGPU)
	other := profile("other", transcodev1alpha1.HardwareAuto)
	encode := func(encoder string) *transcodev1alpha1.Plan {
		return &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeTranscode, Encoder: encoder}
	}
	cand := func(name string, tp *transcodev1alpha1.TranscodeProfile, prio int32, plan *transcodev1alpha1.Plan, fallback string) candidate {
		tj := &transcodev1alpha1.TranscodeJob{}
		tj.Namespace, tj.Name = "ns", name
		tj.Spec.ProfileRef = tp.Name
		tj.Status.Plan, tj.Status.FallbackReason = plan, fallback
		return candidate{tj: tj, tp: tp, slot: Slot{Key: "ns/" + name, Profile: tp.Name, Priority: prio}}
	}
	gpu := map[transcodev1alpha1.Hardware]bool{nv: true}
	slots := map[string]int32{"cpu": 2, "nvidia": 1}
	for _, tc := range []struct {
		name    string
		cands   []candidate
		running []Slot
		gpu     map[transcodev1alpha1.Hardware]bool
		held    map[pool.Key]string
		limits  map[string]int32
		want    map[string]string // key -> class, or "held"
		admits  []string          // what Admit then admits, in order
	}{
		{
			name:   "the first auto job by priority takes the one GPU slot",
			cands:  []candidate{cand("low", auto, 1, encode("libx265"), ""), cand("high", auto, 9, encode("libx265"), "")},
			gpu:    gpu,
			want:   map[string]string{"ns/high": "nvidia", "ns/low": "cpu"},
			admits: []string{"ns/high", "ns/low"},
		},
		{
			name:   "a gpu job waits for the GPU slot rather than taking a cpu one",
			cands:  []candidate{cand("first", gpuOnly, 9, encode("libx265"), ""), cand("second", gpuOnly, 1, encode("libx265"), "")},
			gpu:    gpu,
			want:   map[string]string{"ns/first": "nvidia", "ns/second": "held"},
			admits: []string{"ns/first"},
		},
		{
			name:   "a gpu job a GPU already refused goes to cpu",
			cands:  []candidate{cand("refused", gpuOnly, 9, encode("libx265"), "nvenc failed")},
			gpu:    gpu,
			want:   map[string]string{"ns/refused": "cpu"},
			admits: []string{"ns/refused"},
		},
		{
			name:   "a gpu profile's remux takes a cpu slot",
			cands:  []candidate{cand("remux", gpuOnly, 9, &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeRemuxOnly, Encoder: "copy"}, "")},
			gpu:    gpu,
			want:   map[string]string{"ns/remux": "cpu"},
			admits: []string{"ns/remux"},
		},
		{
			name:   "a gpu job with no GPU node goes to cpu",
			cands:  []candidate{cand("nogpu", gpuOnly, 9, encode("libx265"), "")},
			want:   map[string]string{"ns/nogpu": "cpu"},
			admits: []string{"ns/nogpu"},
		},
		{
			name:    "a dispatched job's slot is not free",
			cands:   []candidate{cand("a", auto, 0, encode("libx265"), "")},
			running: []Slot{{Key: "ns/x", Hardware: "nvidia", Profile: "other"}},
			gpu:     gpu,
			want:    map[string]string{"ns/a": "cpu"},
			admits:  []string{"ns/a"},
		},
		{
			name:   "without a GPU node every auto job is cpu",
			cands:  []candidate{cand("a", auto, 0, encode("libx265"), "")},
			want:   map[string]string{"ns/a": "cpu"},
			admits: []string{"ns/a"},
		},
		{
			name:   "a pinned job ahead takes the GPU slot from an auto job behind it",
			cands:  []candidate{cand("pin", pinned, 9, encode("hevc_nvenc"), ""), cand("a", auto, 1, encode("libx265"), "")},
			gpu:    gpu,
			want:   map[string]string{"ns/pin": "nvidia", "ns/a": "cpu"},
			admits: []string{"ns/pin", "ns/a"},
		},
		{
			name:   "a remux never takes a GPU",
			cands:  []candidate{cand("a", auto, 0, &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeRemuxOnly, Encoder: "copy"}, "")},
			gpu:    gpu,
			want:   map[string]string{"ns/a": "cpu"},
			admits: []string{"ns/a"},
		},
		{
			name:   "a fallback reason keeps an auto job on cpu, whatever its plan says",
			cands:  []candidate{cand("a", auto, 0, encode("hevc_nvenc"), "GPU attempt 1 on nvidia: GPUEncodeFailed")},
			gpu:    gpu,
			want:   map[string]string{"ns/a": "cpu"},
			admits: []string{"ns/a"},
		},
		{
			name:   "R4: an auto job is held for the GPU pool it would use",
			cands:  []candidate{cand("a", auto, 0, encode("libx265"), "")},
			gpu:    gpu,
			held:   map[pool.Key]string{poolKeyFor(auto, nv): "waiting for pool gpu"},
			want:   map[string]string{"ns/a": "held"},
			admits: []string{},
		},
		{
			name:   "R4: an auto job is not held for a GPU pool it would not use",
			cands:  []candidate{cand("a", auto, 0, encode("libx265"), "")},
			held:   map[pool.Key]string{poolKeyFor(auto, nv): "waiting for pool gpu"},
			want:   map[string]string{"ns/a": "cpu"},
			admits: []string{"ns/a"},
		},
		{
			name:   "R4: an auto job bound for a GPU is not held for its draining cpu pool",
			cands:  []candidate{cand("a", auto, 0, encode("libx265"), "")},
			gpu:    gpu,
			held:   map[pool.Key]string{poolKeyFor(auto, cpu): "waiting for pool cpu"},
			want:   map[string]string{"ns/a": "nvidia"},
			admits: []string{"ns/a"},
		},
		{
			name:   "a held job leaves its GPU slot to the next",
			cands:  []candidate{cand("a", auto, 9, encode("libx265"), ""), cand("b", other, 1, encode("libx265"), "")},
			gpu:    gpu,
			held:   map[pool.Key]string{poolKeyFor(auto, nv): "waiting for pool gpu"},
			want:   map[string]string{"ns/a": "held", "ns/b": "nvidia"},
			admits: []string{"ns/b"},
		},
		{
			name:    "a job Admit refuses for its profile's limit leaves its GPU slot to the next",
			cands:   []candidate{cand("a", auto, 9, encode("libx265"), ""), cand("b", other, 1, encode("libx265"), "")},
			running: []Slot{{Key: "ns/x", Hardware: "cpu", Profile: "auto"}},
			gpu:     gpu,
			limits:  map[string]int32{"auto": 1},
			want:    map[string]string{"ns/a": "nvidia", "ns/b": "nvidia"},
			admits:  []string{"ns/b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Reconciler{Slots: slots}
			queued, holds := r.assignClasses(tc.cands, tc.running, tc.gpu, tc.held, tc.limits)
			got := map[string]string{}
			for _, q := range queued {
				got[q.slot.Key] = q.slot.Hardware
			}
			for _, h := range holds {
				got[h.tj.Namespace+"/"+h.tj.Name] = "held"
			}
			assert.Equal(t, tc.want, got)

			var in []Slot
			for _, q := range queued {
				in = append(in, q.slot)
			}
			admitted := []string{}
			for _, s := range Admit(in, tc.running, Budget{Slots: slots, ProfileLimits: tc.limits}) {
				admitted = append(admitted, s.Key)
			}
			assert.Equal(t, tc.admits, admitted)
		})
	}
}

// TestGPUNodesReadTheConfiguredLabels: a GPU node is found by the label the
// class's pools are held to -- --gpu-node-label-nvidia and
// --gpu-node-label-intel, through pool.Config -- set to "true", with the
// resource its pods request allocatable; the default label means nothing
// once another is configured.
func TestGPUNodesReadTheConfiguredLabels(t *testing.T) {
	node := func(name string, labels map[string]string, res corev1.ResourceName) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{res: resource.MustParse("1")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		node("nv-default", map[string]string{pool.DefaultNodeLabelNVIDIA: "true"}, "nvidia.com/gpu"),
		node("intel-custom", map[string]string{"example.com/igpu": "true"}, "gpu.intel.com/i915"),
		node("intel-false", map[string]string{"example.com/igpu": "false"}, "gpu.intel.com/i915"),
	).Build()

	r := &Reconciler{Client: c, Pool: pool.Config{NodeLabelIntel: "example.com/igpu"}}
	got, err := r.gpuNodes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[transcodev1alpha1.Hardware]bool{
		transcodev1alpha1.HardwareNVIDIA: true, transcodev1alpha1.HardwareIntel: true,
	}, got)

	r.Pool.NodeLabelNVIDIA = "example.com/dgpu"
	got, err = r.gpuNodes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[transcodev1alpha1.Hardware]bool{transcodev1alpha1.HardwareIntel: true}, got,
		"with another label configured, the default one marks no GPU node")
}

// TestUnschedulableSince is when the longest-waiting live pod began to
// wait: a scheduled, running or deleting pod, or one waiting for another
// reason (a scheduling gate), is not waiting for a node.
func TestUnschedulableSince(t *testing.T) {
	at := func(m int) metav1.Time {
		return metav1.NewTime(metav1.Now().Add(-1 * time.Duration(m) * time.Minute).Truncate(time.Second))
	}
	pod := func(phase corev1.PodPhase, reason string, since metav1.Time, deleting bool) corev1.Pod {
		p := corev1.Pod{Status: corev1.PodStatus{Phase: phase, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: reason, LastTransitionTime: since,
		}}}}
		if deleting {
			p.DeletionTimestamp = &since
		}
		return p
	}
	t20, t15, t5 := at(20), at(15), at(5)
	assert.True(t, unschedulableSince(nil).IsZero())
	assert.Equal(t, t15.Time, unschedulableSince([]corev1.Pod{
		pod(corev1.PodPending, corev1.PodReasonUnschedulable, t5, false),
		pod(corev1.PodPending, corev1.PodReasonUnschedulable, t15, false),
		pod(corev1.PodPending, corev1.PodReasonUnschedulable, t20, true),    // going
		pod(corev1.PodPending, corev1.PodReasonSchedulingGated, t20, false), // gated, not unschedulable
		pod(corev1.PodRunning, corev1.PodReasonUnschedulable, t20, false),   // stale condition on a running pod
	}))
}

// One healthy node keeps a class: work may land on it. No report is not
// unhealthy.
func TestAClassIsUnhealthyOnlyWhenEveryReportSaysSo(t *testing.T) {
	_, ok := unhealthyMessage(transcodev1alpha1.HardwareNVIDIA, nil)
	assert.False(t, ok, "no report")
	_, ok = unhealthyMessage(transcodev1alpha1.HardwareNVIDIA, map[string]task.NodeHealth{
		"a": {Healthy: false, Error: "no device"}, "b": {Healthy: true},
	})
	assert.False(t, ok, "one node can still take work")
	msg, ok := unhealthyMessage(transcodev1alpha1.HardwareNVIDIA, map[string]task.NodeHealth{
		"b": {Healthy: false, Error: "busy"}, "a": {Healthy: false, Error: "no device"},
	})
	assert.True(t, ok)
	assert.Contains(t, msg, "a: no device", "the first node by name, with its reason")
}
