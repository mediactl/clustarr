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

package downloadclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const (
	mbit = int64(1_000_000)
	gbit = 1000 * mbit
)

func quantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

func node(annotations map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Annotations: annotations}}
}

// An engine's request and limit are its node's own annotations where it
// states them, else the client's percentages of the node's link, else of
// the default link.
func TestNodeLinkReadsTheNodeThenThePercentages(t *testing.T) {
	bw := &downloadv1alpha1.BandwidthSpec{}
	assert.Equal(t, link{request: 250 * mbit, limit: 500 * mbit}, nodeLink(nil, bw),
		"no node: a 1G link at 25% and 50%")
	assert.Equal(t, link{request: 625 * mbit, limit: 1250 * mbit},
		nodeLink(node(map[string]string{downloadv1alpha1.NodeLinkSpeedAnnotation: "2500M"}), bw))
	assert.Equal(t, link{request: 625 * mbit, limit: 2 * gbit},
		nodeLink(node(map[string]string{
			downloadv1alpha1.NodeLinkSpeedAnnotation:      "2500M",
			downloadv1alpha1.NodeBandwidthLimitAnnotation: "2G",
		}), bw), "the node's own limit stands in place of the percentage")
	assert.Equal(t, link{request: 100 * mbit, limit: 100 * mbit},
		nodeLink(node(map[string]string{downloadv1alpha1.NodeBandwidthLimitAnnotation: "100M"}), bw),
		"a request never passes the limit")
	assert.Equal(t, link{request: gbit, limit: 8 * gbit},
		nodeLink(node(map[string]string{downloadv1alpha1.NodeLinkSpeedAnnotation: "garbage"}),
			&downloadv1alpha1.BandwidthSpec{DefaultLinkSpeed: quantity("10G"), RequestPercent: 10, LimitPercent: 80}),
		"an unreadable annotation is the default link")
}

// The talos fleet of the design (§5.2): a 2.5G node whose limit is raised
// to 2G and a 1G node on the percentages, under a total of 4G.
func TestComputeBudgetsOnTheDesignsExample(t *testing.T) {
	links := map[int32]link{
		0: {request: 625 * mbit, limit: 2 * gbit},
		1: {request: 250 * mbit, limit: 500 * mbit},
	}
	bw := &downloadv1alpha1.BandwidthSpec{Total: quantity("4G")}
	both := computeBudgets(links, map[int32]bool{0: true, 1: true}, bw, 8)
	assert.Equal(t, budgetOf{bits: 2 * gbit, limit: 2 * gbit, transfers: 8}, both[0])
	assert.Equal(t, budgetOf{bits: 500 * mbit, limit: 500 * mbit, transfers: 2}, both[1])

	// An engine with nothing to do holds its request and no more.
	one := computeBudgets(links, map[int32]bool{0: true}, bw, 8)
	assert.Equal(t, int64(2*gbit), one[0].bits)
	assert.Equal(t, budgetOf{bits: 250 * mbit, limit: 500 * mbit, transfers: 1}, one[1])
}

// The total is never passed: what is left after the requests is shared in
// proportion to each engine's room, and requests that alone pass it are
// scaled down together.
func TestComputeBudgetsHoldsTheTotal(t *testing.T) {
	links := map[int32]link{
		0: {request: 600 * mbit, limit: 2 * gbit},   // room 1400
		1: {request: 200 * mbit, limit: 800 * mbit}, // room 600
	}
	engaged := map[int32]bool{0: true, 1: true}
	got := computeBudgets(links, engaged, &downloadv1alpha1.BandwidthSpec{Total: quantity("1800M")}, 32)
	// 1000M left over 2000M of room: half of each engine's room.
	assert.Equal(t, 1300*mbit, got[0].bits)
	assert.Equal(t, 500*mbit, got[1].bits)
	assert.Equal(t, 1800*mbit, got[0].bits+got[1].bits)
	assert.Equal(t, int32(5), got[0].transfers, "1300M over 250M a transfer, rounded")
	assert.Equal(t, int32(2), got[1].transfers)

	tight := computeBudgets(links, engaged, &downloadv1alpha1.BandwidthSpec{Total: quantity("400M")}, 32)
	assert.Equal(t, 300*mbit, tight[0].bits)
	assert.Equal(t, 100*mbit, tight[1].bits)
	assert.Equal(t, int32(1), tight[1].transfers, "never fewer than one transfer")

	none := computeBudgets(links, engaged, &downloadv1alpha1.BandwidthSpec{}, 4)
	assert.Equal(t, 2*gbit, none[0].bits, "no total: every engaged engine has its limit")
	assert.Equal(t, int32(4), none[0].transfers, "never more than maxActive")
}

func TestScale(t *testing.T) {
	zero, one := int32(0), int32(1)
	cap8 := map[int32]int32{0: 8, 1: 2, 2: 2}
	for _, tc := range []struct {
		name     string
		in       scaleInput
		want     int32
		draining *int32
	}{
		{"waiters and every engine full adds one",
			scaleInput{current: 1, min: 1, max: 4, waiting: 5, pinned: map[int32]int{0: 8}, capacity: cap8, ready: true}, 2, nil},
		{"a free slot adds none: the waiters are about to be assigned",
			scaleInput{current: 1, min: 1, max: 4, waiting: 5, pinned: map[int32]int{0: 7}, capacity: cap8, ready: true}, 1, nil},
		{"none is added while one is starting",
			scaleInput{current: 2, min: 1, max: 4, waiting: 5, pinned: map[int32]int{0: 8, 1: 2}, capacity: cap8, ready: false}, 2, nil},
		{"the maximum holds",
			scaleInput{current: 2, min: 1, max: 2, waiting: 5, pinned: map[int32]int{0: 8, 1: 2}, capacity: cap8, ready: true}, 2, nil},
		{"waiters take a draining ordinal back before another is added",
			scaleInput{current: 2, min: 1, max: 4, waiting: 1, pinned: map[int32]int{0: 8, 1: 0}, capacity: cap8, ready: true, draining: &one}, 2, nil},
		{"nothing waits and the rest can carry it all: the highest ordinal drains",
			scaleInput{current: 2, min: 1, max: 4, pinned: map[int32]int{0: 3, 1: 2}, capacity: cap8, ready: true}, 2, &one},
		{"more is pinned than the rest can carry: nothing drains",
			scaleInput{current: 2, min: 1, max: 4, pinned: map[int32]int{0: 8, 1: 2}, capacity: cap8, ready: true}, 2, nil},
		{"a draining ordinal that still holds something stays",
			scaleInput{current: 2, min: 1, max: 4, pinned: map[int32]int{0: 3}, capacity: cap8, ready: true, draining: &one, idleFor: 0}, 2, &one},
		{"a draining ordinal idle for the grace is removed",
			scaleInput{current: 2, min: 1, max: 4, pinned: map[int32]int{0: 3}, capacity: cap8, ready: true, draining: &one, idleFor: drainIdle}, 1, nil},
		{"the minimum holds",
			scaleInput{current: 1, min: 1, max: 4, pinned: map[int32]int{}, capacity: cap8, ready: true}, 1, nil},
		{"a minimum of zero drains the last engine of an idle client",
			scaleInput{current: 1, min: 0, max: 4, pinned: map[int32]int{}, capacity: cap8, ready: true}, 1, &zero},
		{"and removes it",
			scaleInput{current: 1, min: 0, max: 4, pinned: map[int32]int{}, capacity: cap8, ready: true, draining: &zero, idleFor: time.Hour}, 0, nil},
		{"a waiter starts the first engine of a client at none",
			scaleInput{current: 0, min: 0, max: 4, waiting: 1, pinned: map[int32]int{}, capacity: cap8, ready: true}, 1, nil},
		{"a count outside the bounds is brought inside them",
			scaleInput{current: 5, min: 1, max: 3, pinned: map[int32]int{}, capacity: cap8, ready: true}, 3, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, draining := scale(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.draining, draining)
		})
	}
}

// One engine keeps the working area it has always had; a fleet's engines
// each get their own: a claim per ordinal for a storage class, a directory
// per pod on the shared volume.
func TestAFleetsEnginesEachHaveTheirOwnWorkingArea(t *testing.T) {
	sc := "local-path"
	single := usenetClient("nzb")
	single.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{StorageClassName: &sc}
	assert.False(t, scratchPerReplica(single))
	assert.True(t, needsScratchClaim(single), "one engine keeps its companion claim")

	fleet := usenetClient("nzb")
	fleet.Spec.Replicas = 2
	fleet.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{StorageClassName: &sc}
	require.True(t, scratchPerReplica(fleet))
	assert.False(t, needsScratchClaim(fleet))
	sts := buildUsenetStatefulSet(fleet, "nzb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
	assert.Equal(t, int32(2), *sts.Spec.Replicas)
	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	assert.Equal(t, scratchVolumeName, *sts.Spec.VolumeClaimTemplates[0].Name)
	mounted := false
	for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		mounted = mounted || (*m.Name == scratchVolumeName && *m.MountPath == "/scratch")
	}
	assert.True(t, mounted, "the claim template is mounted as the working area")

	autoscaled := usenetClient("nzb")
	autoscaled.Spec.Usenet.Autoscale = &downloadv1alpha1.EngineAutoscale{MaxReplicas: 3}
	autoscaled.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/usenet/incomplete"}
	_, args := EngineCommand(autoscaled, "/data", "/scratch")
	assert.Contains(t, args, "--scratch-dir=/data/usenet/incomplete/$(POD_NAME)",
		"the kubelet expands the pod's name: an engine re-attaches its own directory")
	assert.Contains(t, args, "--download-client=nzb", "a fleet's engine reads its ordinal from its pod's name")
	assert.NotContains(t, args, "--engine=nzb-0")
	single.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/usenet/incomplete"}
	_, args = EngineCommand(single, "/data", "/scratch")
	assert.Contains(t, args, "--scratch-dir=/data/usenet/incomplete")
	assert.Contains(t, args, "--engine=nzb-0", "one engine is told its identity, as it always was")
}

// A usenet engine is a StatefulSet pod: its ordinal is its identity, one
// engine runs on a node, and the pods start together.
func TestUsenetEnginesRunOneANode(t *testing.T) {
	sts := buildUsenetStatefulSet(usenetClient("nzb"), "nzb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
	require.NotNil(t, sts.Spec.PodManagementPolicy)
	assert.Equal(t, appsv1.ParallelPodManagement, *sts.Spec.PodManagementPolicy)
	anti := sts.Spec.Template.Spec.Affinity.PodAntiAffinity
	require.NotNil(t, anti)
	require.Len(t, anti.RequiredDuringSchedulingIgnoredDuringExecution, 1)
	term := anti.RequiredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, corev1.LabelHostname, *term.TopologyKey)
	assert.Equal(t, selectorLabels(usenetClient("nzb")), term.LabelSelector.MatchLabels)
}

// The bandwidth budget and the engine count are the controller's: changing
// either restarts no engine. A connection pool is read at start and does.
func TestTheConfigHashIgnoresWhatOnlyTheControllerReads(t *testing.T) {
	dc := usenetClient("nzb")
	base := engineConfigHash(dc, nil)
	dc.Spec.Usenet.Bandwidth = &downloadv1alpha1.BandwidthSpec{Total: quantity("4G")}
	dc.Spec.Usenet.Autoscale = &downloadv1alpha1.EngineAutoscale{MinReplicas: ptr.To(int32(0)), MaxReplicas: 3}
	assert.Equal(t, base, engineConfigHash(dc, nil))
	dc.Spec.Usenet.ConnectionPools = []downloadv1alpha1.ConnectionPool{{Name: "frugal", Connections: 100}}
	assert.NotEqual(t, base, engineConfigHash(dc, nil))
}

func TestAPoolIsOneAccount(t *testing.T) {
	dc := usenetClient("nzb")
	ref := func(n string) corev1.LocalObjectReference { return corev1.LocalObjectReference{Name: n} }
	dc.Spec.Usenet.Providers = []downloadv1alpha1.NNTPProvider{
		{Name: "us", Host: "news.example", Pool: "p", SecretRef: ref("a")},
		{Name: "eu", Host: "eunews.example", Pool: "p", SecretRef: ref("a")},
		{Name: "other", Host: "other.example", SecretRef: ref("b")},
	}
	require.NoError(t, validatePools(dc))
	dc.Spec.Usenet.Providers[1].SecretRef = ref("b")
	assert.ErrorContains(t, validatePools(dc), `connection pool "p" is one account`)
}

// Only a client with a budget or several engines binds late; the single
// engine every install has today is told everything at once, as before.
func TestLateBindingIsOptIn(t *testing.T) {
	dc := usenetClient("nzb")
	assert.False(t, lateBinding(dc))
	assert.False(t, usenetFleet(dc))
	dc.Spec.Usenet.Bandwidth = &downloadv1alpha1.BandwidthSpec{}
	assert.True(t, lateBinding(dc))
	assert.False(t, usenetFleet(dc), "a budget alone is still one engine")
	dc.Spec.Usenet.Bandwidth = nil
	dc.Spec.Replicas = 2
	assert.True(t, lateBinding(dc))
	assert.True(t, usenetFleet(dc))
	assert.False(t, lateBinding(torrentClient("qbt", 3)))
}

// Status reports a quota's use in steps, so a download in progress does
// not rewrite it at every reconcile: a hundredth of the quota, never finer
// than a gibibyte.
func TestQuotaUseIsReportedInSteps(t *testing.T) {
	const gib = int64(1) << 30
	assert.Equal(t, int64(0), quotaStep(gib-1, 300*gib))
	assert.Equal(t, 3*gib, quotaStep(5*gib, 300*gib), "a hundredth of 300 GiB is 3 GiB")
	assert.Equal(t, 297*gib, quotaStep(299*gib, 300*gib))
	assert.Equal(t, 4*gib, quotaStep(4*gib+gib/2, 10*gib), "a small quota moves by the gibibyte")
}

// The downloads stage's signal reaches the controller's queue once it has
// started, and is nothing before that or on a nil Wake.
func TestTheWakeEnqueuesTheClient(t *testing.T) {
	var none *Wake
	none.Signal("media", "nzb")
	w := NewWake()
	w.Signal("media", "early")
	q := &requestQueue{}
	require.NoError(t, w.Start(t.Context(), q))
	w.Signal("media", "nzb")
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "media", Name: "nzb"}}}, q.added)
}

type requestQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	added []reconcile.Request
}

func (q *requestQueue) Add(r reconcile.Request) { q.added = append(q.added, r) }

// Only a node's bandwidth annotations wake the clients it budgets: its
// status, which changes every few minutes, does not.
func TestOnlyANodesBandwidthAnnotationsWake(t *testing.T) {
	p := nodeBandwidthChanged()
	plain := node(nil)
	fast := node(map[string]string{downloadv1alpha1.NodeLinkSpeedAnnotation: "2500M"})
	faster := node(map[string]string{downloadv1alpha1.NodeLinkSpeedAnnotation: "10G"})
	heartbeat := fast.DeepCopy()
	heartbeat.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	other := node(map[string]string{"example.com/unrelated": "x"})

	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: fast, ObjectNew: heartbeat}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: other}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: fast, ObjectNew: faster}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: fast}))
	assert.True(t, p.Create(event.CreateEvent{Object: fast}))
	assert.False(t, p.Create(event.CreateEvent{Object: plain}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: fast}))
}

// A status that says what the stored one says is not applied again: an
// engine's record wakes the reconcile every minute, and that wake must cost
// the apiserver nothing. A nil and an empty list are one; any field that
// differs is a change.
func TestAnUnchangedStatusIsNotApplied(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	have := &downloadv1alpha1.DownloadClientStatus{
		ObservedGeneration: 3,
		Engine:             &downloadv1alpha1.EngineStatus{WorkloadRef: "nzb-engine", Replicas: 1, ReadyReplicas: 1, DesiredReplicas: ptr.To(int32(1))},
		Active:             2, FreeBytes: 50 << 30,
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reconciled", Message: "ok", LastTransitionTime: at, ObservedGeneration: 3}},
		Budgets:    []downloadv1alpha1.EngineBudgetStatus{},
	}
	want := func() *downloadac.DownloadClientStatusApplyConfiguration {
		return downloadac.DownloadClientStatus().
			WithObservedGeneration(3).
			WithEngine(downloadac.EngineStatus().WithWorkloadRef("nzb-engine").WithReplicas(1).WithReadyReplicas(1).WithDesiredReplicas(1)).
			WithActive(2).WithQueued(0).WithFreeBytes(50 << 30).
			WithConditions(k8s.ConditionACs(have.Conditions)...)
	}
	assert.True(t, statusUnchanged(want(), have))
	assert.False(t, statusUnchanged(want().WithActive(3), have))
	assert.False(t, statusUnchanged(want().WithEngine(downloadac.EngineStatus().WithWorkloadRef("nzb-engine").WithReplicas(1).WithReadyReplicas(1).WithDesiredReplicas(2)), have))
	assert.False(t, statusUnchanged(want().WithBudgets(downloadac.EngineBudgetStatus().WithEngine("nzb-0").WithTransfers(2)), have))
	assert.False(t, statusUnchanged(want(), &downloadv1alpha1.DownloadClientStatus{}), "nothing stored yet")
}
