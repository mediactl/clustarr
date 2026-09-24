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
		want           transcodev1alpha1.Hardware
	}{
		{"nvidia first", both, nil, slots, false, nv},
		{"intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, in},
		{"cpu when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, cpu},
		{"cpu without a GPU node", nil, nil, slots, false, cpu},
		{"intel without an nvidia node", map[transcodev1alpha1.Hardware]bool{in: true}, nil, slots, false, in},
		{"skip an unschedulable pool", both, map[transcodev1alpha1.Hardware]bool{nv: true}, slots, false, in},
		{"a fallback reason pins cpu", both, nil, slots, true, cpu},
		{"an over-budget class is full, not free", both, nil, map[transcodev1alpha1.Hardware]int32{nv: -1, in: 0}, false, cpu},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseClass(tc.nodes, tc.unsched, tc.free, tc.fallback))
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
