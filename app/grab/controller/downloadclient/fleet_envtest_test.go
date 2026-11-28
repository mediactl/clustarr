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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// apiUsenetClient is a usenet client the apiserver accepts: in the test
// namespace, with the one provider the API requires.
func apiUsenetClient(name string) *downloadv1alpha1.DownloadClient {
	dc := usenetClient(name)
	dc.Namespace = testNS
	dc.Spec.Usenet.Providers = []downloadv1alpha1.NNTPProvider{{
		Name: "primary", Host: "news.example.com", SecretRef: corev1.LocalObjectReference{Name: "nntp-creds"},
	}}
	return dc
}

// readyNode creates a schedulable, Ready node.
func (e *dcEnv) readyNode(name string, annotations map[string]string) {
	e.t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
	require.NoError(e.t, e.api.Create(e.ctx, n))
	// The apiserver taints a new node not-ready, and nothing here plays the
	// node controller that takes the taint off.
	n.Spec.Taints = nil
	require.NoError(e.t, e.api.Update(e.ctx, n))
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	require.NoError(e.t, e.api.Status().Update(e.ctx, n))
	require.Eventually(e.t, func() bool {
		var got corev1.Node
		return e.c.Get(e.ctx, types.NamespacedName{Name: name}, &got) == nil && len(got.Status.Conditions) == 1 && len(got.Spec.Taints) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func (e *dcEnv) statefulSetReplicas(name string) int32 {
	e.t.Helper()
	var s appsv1.StatefulSet
	require.NoError(e.t, e.api.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: engineWorkloadName(name)}, &s))
	return ptr.Deref(s.Spec.Replicas, -1)
}

func (e *dcEnv) budget(name string, ordinal int32) (schema.EngineBudget, bool) {
	e.t.Helper()
	en, err := e.bus.KV(cevents.BucketEngineBudgets).Get(e.ctx, cevents.EngineBudgetKey(name, ordinal))
	if err != nil || len(en.Value) == 0 {
		return schema.EngineBudget{}, false
	}
	var b schema.EngineBudget
	require.NoError(e.t, json.Unmarshal(en.Value, &b))
	return b, true
}

// Two engines on two nodes under usenet.bandwidth: each is budgeted from
// its own node -- the node's annotations where it states them, the
// client's percentages of a default link where it states none -- the
// budget reaches the engine through its key in clustarr-engine-budgets,
// and status reports it. Removing the bandwidth removes the budgets.
func TestAUsenetFleetIsBudgetedFromItsNodes(t *testing.T) {
	e := newDCEnv(t)
	e.readyNode("big", map[string]string{
		downloadv1alpha1.NodeLinkSpeedAnnotation:      "2500M",
		downloadv1alpha1.NodeBandwidthLimitAnnotation: "2G",
	})
	e.readyNode("small", nil)
	dc := apiUsenetClient("nzb")
	dc.Spec.Replicas = 2
	dc.Spec.Usenet.MaxActive = ptr.To(int32(8))
	dc.Spec.Usenet.Bandwidth = &downloadv1alpha1.BandwidthSpec{Total: quantity("4G")}
	dc = e.client(dc)

	res := e.reconcile("nzb")
	assert.Equal(t, fleetInterval, res.RequeueAfter, "a budgeted client is reconciled often: its budgets follow which engines are busy")
	require.Equal(t, int32(2), e.statefulSetReplicas("nzb"))
	e.readyWorkload(dc, 2)
	e.record(dc, 0, func(r *schema.EngineRecord) { r.Node, r.Active, r.DownRateBytesPerSec = "big", 3, 100_000_000 })
	e.record(dc, 1, func(r *schema.EngineRecord) { r.Node, r.Queued = "small", 1 })
	e.reconcile("nzb")

	got := e.get("nzb")
	require.Len(t, got.Status.Budgets, 2)
	assert.Equal(t, downloadv1alpha1.EngineBudgetStatus{Engine: "nzb-0", Node: "big", BudgetBps: 2_000_000_000, Transfers: 8, RateBps: 800_000_000}, got.Status.Budgets[0])
	assert.Equal(t, downloadv1alpha1.EngineBudgetStatus{Engine: "nzb-1", Node: "small", BudgetBps: 500_000_000, Transfers: 2}, got.Status.Budgets[1])
	b0, ok := e.budget("nzb", 0)
	require.True(t, ok)
	assert.Equal(t, int64(2_000_000_000), b0.BitsPerSecond)
	assert.Equal(t, int32(8), b0.Transfers)
	assert.Equal(t, int64(2000), b0.Weight, "the weight is the budget in megabits")
	b1, ok := e.budget("nzb", 1)
	require.True(t, ok)
	assert.Equal(t, int64(500), b1.Weight)

	// An engine with nothing to do falls back to its request, and the
	// budget is written again only because it changed.
	e.record(dc, 1, func(r *schema.EngineRecord) { r.Node = "small" })
	e.reconcile("nzb")
	b1again, _ := e.budget("nzb", 1)
	assert.Equal(t, int64(250_000_000), b1again.BitsPerSecond)
	seq := b0.Seq
	b0again, _ := e.budget("nzb", 0)
	assert.Equal(t, seq, b0again.Seq, "an unchanged budget is not rewritten")

	// No bandwidth: no budgets, in the bucket or in status.
	live := e.get("nzb")
	live.Spec.Usenet.Bandwidth = nil
	require.NoError(t, e.api.Update(e.ctx, live))
	require.Eventually(t, func() bool {
		var c downloadv1alpha1.DownloadClient
		return e.c.Get(e.ctx, client.ObjectKeyFromObject(live), &c) == nil && c.Spec.Usenet.Bandwidth == nil
	}, 10*time.Second, 10*time.Millisecond)
	e.reconcile("nzb")
	_, ok = e.budget("nzb", 0)
	assert.False(t, ok)
	assert.Empty(t, e.get("nzb").Status.Budgets)
}

// seedSlots replaces movie's entries with holders pinned to engines and
// waiters on client, and waits for the cache's index to show them.
func (e *dcEnv) seedSlots(movie *catalogv1alpha1.Movie, clientName string, holders []string, waiters int) {
	e.t.Helper()
	var entries []catalogv1alpha1.DownloadEntry
	for i, eng := range holders {
		en := itemtest.Entry(movie.Name+"-h"+string(rune('a'+i)), commonv1alpha1.DownloadPhaseDownloading)
		en.Client, en.Engine = clientName, eng
		entries = append(entries, en)
	}
	for i := range waiters {
		en := itemtest.Entry(movie.Name+"-w"+string(rune('a'+i)), commonv1alpha1.DownloadPhasePending)
		en.Client, en.Engine = clientName, ""
		en.Dispatch.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryWaiting, Reason: catalogv1alpha1.DeliveryReasonNoSlot}
		entries = append(entries, en)
	}
	require.NoError(e.t, itemtest.SeedEntries(e.ctx, e.api, movie, entries...))
	require.Eventually(e.t, func() bool {
		w, err := dlindex.SlotWaiters(e.ctx, e.c, testNS, clientName)
		if err != nil || len(w) != waiters {
			return false
		}
		n := 0
		for _, eng := range []string{clientName + "-0", clientName + "-1", clientName + "-2"} {
			h, err := dlindex.SlotHolders(e.ctx, e.c, testNS, eng)
			if err != nil {
				return false
			}
			n += len(h)
		}
		return n == len(holders)
	}, 10*time.Second, 10*time.Millisecond, "the cache's index sees the slots")
}

// waitStatus reconciles until the cached client's status satisfies ok: the
// controller reads its own last answer (the count, the draining ordinal)
// from the cache.
func (e *dcEnv) waitStatus(name string, ok func(*downloadv1alpha1.DownloadClient) bool) {
	e.t.Helper()
	require.Eventually(e.t, func() bool {
		e.reconcile(name)
		var c downloadv1alpha1.DownloadClient
		return e.c.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: name}, &c) == nil && ok(&c)
	}, 15*time.Second, 50*time.Millisecond)
}

// usenet.autoscale: an engine is added while a download waits for a slot
// and every engine is full, never past the maximum or before the last one
// is ready; when the others can carry what is left, the highest ordinal is
// drained and, once nothing is pinned to it for the grace, removed.
func TestAutoscaleAddsAnEngineForWaitersAndDrainsTheLastWhenIdle(t *testing.T) {
	e := newDCEnv(t)
	for _, n := range []string{"n1", "n2", "n3"} {
		e.readyNode(n, nil)
	}
	dc := apiUsenetClient("nzb")
	dc.Spec.Usenet.MaxActive = ptr.To(int32(1))
	dc.Spec.Usenet.Autoscale = &downloadv1alpha1.EngineAutoscale{MaxReplicas: 2}
	dc = e.client(dc)
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-1995", Namespace: testNS},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, e.c.Create(e.ctx, movie))

	e.reconcile("nzb")
	require.Equal(t, int32(1), e.statefulSetReplicas("nzb"), "the minimum, one by default")
	e.readyWorkload(dc, 1)
	e.record(dc, 0, nil)

	// One engine, its one slot held, and a download waiting: a second.
	e.seedSlots(movie, "nzb", []string{"nzb-0"}, 2)
	e.waitStatus("nzb", func(c *downloadv1alpha1.DownloadClient) bool {
		return c.Status.Engine != nil && ptr.Deref(c.Status.Engine.DesiredReplicas, 0) == 2
	})
	require.Equal(t, int32(2), e.statefulSetReplicas("nzb"))
	assert.Equal(t, int32(2), e.get("nzb").Status.Engine.Waiting)
	require.Len(t, e.durables("nzb"), 2, "the new ordinal has its durable")

	// Both full and one still waiting: the maximum holds.
	e.readyWorkload(dc, 2)
	e.record(dc, 1, nil)
	e.seedSlots(movie, "nzb", []string{"nzb-0", "nzb-1"}, 1)
	e.reconcile("nzb")
	e.reconcile("nzb")
	require.Equal(t, int32(2), e.statefulSetReplicas("nzb"))
	assert.Nil(t, e.get("nzb").Status.Engine.Draining, "nothing drains while a download waits")

	// Nothing waits and the first engine can carry what is left: the
	// second drains, and stays while something is pinned to it.
	e.seedSlots(movie, "nzb", []string{"nzb-0"}, 0)
	e.waitStatus("nzb", func(c *downloadv1alpha1.DownloadClient) bool {
		return c.Status.Engine != nil && ptr.Deref(c.Status.Engine.Draining, -1) == 1
	})
	e.reconcile("nzb") // the idle clock starts
	e.advance(drainIdle - time.Minute)
	e.record(dc, 0, nil)
	e.record(dc, 1, nil)
	e.reconcile("nzb")
	require.Equal(t, int32(2), e.statefulSetReplicas("nzb"), "not before the grace")

	e.advance(2 * time.Minute)
	e.record(dc, 0, nil)
	e.record(dc, 1, nil)
	e.waitStatus("nzb", func(c *downloadv1alpha1.DownloadClient) bool {
		return c.Status.Engine != nil && ptr.Deref(c.Status.Engine.DesiredReplicas, 0) == 1 && c.Status.Engine.Draining == nil
	})
	require.Equal(t, int32(1), e.statefulSetReplicas("nzb"))
}

// The API's own rules for a fleet: a usenet client may have several
// replicas; a provider's pool must be declared; one bound claim cannot
// serve several engines.
func TestTheAPIValidatesAFleet(t *testing.T) {
	e := newDCEnv(t)
	mk := func(name string, mutate func(*downloadv1alpha1.DownloadClient)) error {
		dc := apiUsenetClient(name)
		mutate(dc)
		return e.api.Create(e.ctx, dc)
	}
	require.NoError(t, mk("several", func(dc *downloadv1alpha1.DownloadClient) { dc.Spec.Replicas = 3 }))

	require.NoError(t, mk("pooled", func(dc *downloadv1alpha1.DownloadClient) {
		dc.Spec.Usenet.ConnectionPools = []downloadv1alpha1.ConnectionPool{{Name: "plan", Connections: 100}}
		dc.Spec.Usenet.Providers[0].Pool = "plan"
	}))
	err := mk("stray", func(dc *downloadv1alpha1.DownloadClient) { dc.Spec.Usenet.Providers[0].Pool = "plan" })
	require.ErrorContains(t, err, "a provider's pool must name an entry of connectionPools")

	bound := func(dc *downloadv1alpha1.DownloadClient) {
		dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{VolumeName: "nvme"}
	}
	require.NoError(t, mk("one-claim", bound), "one engine on one bound volume, as before")
	err = mk("two-on-one-claim", func(dc *downloadv1alpha1.DownloadClient) {
		bound(dc)
		dc.Spec.Replicas = 2
	})
	require.ErrorContains(t, err, "need replicas == 1 and no autoscale")
	err = mk("scaled-on-one-claim", func(dc *downloadv1alpha1.DownloadClient) {
		dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{ExistingClaim: "mine"}
		dc.Spec.Usenet.Autoscale = &downloadv1alpha1.EngineAutoscale{MaxReplicas: 2}
	})
	require.ErrorContains(t, err, "need replicas == 1 and no autoscale")

	err = mk("inverted", func(dc *downloadv1alpha1.DownloadClient) {
		dc.Spec.Usenet.Autoscale = &downloadv1alpha1.EngineAutoscale{MinReplicas: ptr.To(int32(3)), MaxReplicas: 2}
	})
	require.ErrorContains(t, err, "minReplicas must not exceed maxReplicas")

	// A provider that states no connections is stored with none: the
	// engine reads that as eight, or as its pool's whole size.
	var got downloadv1alpha1.DownloadClient
	require.NoError(t, e.api.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: "pooled"}, &got))
	assert.Zero(t, got.Spec.Usenet.Providers[0].Connections)
	assert.Equal(t, downloadv1alpha1.QuotaPeriodMonth, got.Spec.Usenet.Providers[0].QuotaPeriod)
}
