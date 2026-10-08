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
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

const testNS = "default"

// dcEnv is the DownloadClient controller against a real apiserver
// (envtest), reading through a cache that carries dlindex's field index (the
// manager's), over a membus bus whose StreamAdmin holds the engine durables
// and whose clustarr-engines bucket the test writes engine records into.
type dcEnv struct {
	t         *testing.T
	ctx       context.Context
	cfg       *rest.Config
	c         client.Client // cached reads, direct writes: the manager's client
	api       client.Client // uncached
	bus       *membus.Bus
	engines   *records.Writer[*schema.EngineRecord]
	rec       *events.FakeRecorder
	engineRec *events.FakeRecorder
	r         *Reconciler

	mu  sync.Mutex
	now time.Time
}

func newDCEnv(t *testing.T) *dcEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		_ = env.Stop()
	})
	scheme := k8s.MustNewScheme()
	ca, err := cache.New(cfg, cache.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, dlindex.Register(ctx, ca))
	go func() { _ = ca.Start(ctx) }()
	require.True(t, ca.WaitForCacheSync(ctx))
	c, err := client.New(cfg, client.Options{Scheme: scheme, Cache: &client.CacheOptions{Reader: ca}})
	require.NoError(t, err)
	api, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, cevents.Default()))

	e := &dcEnv{
		t: t, ctx: ctx, cfg: cfg, c: c, api: api, bus: bus,
		engines:   records.NewWriter(bus.KV(cevents.BucketEngines), agentrecords.Engines(), "engine-test", "test"),
		rec:       events.NewFakeRecorder(100),
		engineRec: events.NewFakeRecorder(100),
		now:       time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
	}
	e.r = NewReconciler(c, e.rec, "/data", "/scratch", "ghcr.io/x/native:dev")
	e.r.Bus, e.r.Admin = bus, bus
	e.r.Engines = records.NewReader(bus.KV(cevents.BucketEngines), agentrecords.Engines())
	e.r.EngineRecorder = e.engineRec
	e.r.MinFreeBytes = 10 << 30
	e.r.Now = e.clock
	return e
}

func (e *dcEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *dcEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// client creates dc and waits for the cache to hold it.
func (e *dcEnv) client(dc *downloadv1alpha1.DownloadClient) *downloadv1alpha1.DownloadClient {
	e.t.Helper()
	require.NoError(e.t, e.c.Create(e.ctx, dc))
	require.Eventually(e.t, func() bool {
		return e.c.Get(e.ctx, client.ObjectKeyFromObject(dc), &downloadv1alpha1.DownloadClient{}) == nil
	}, 10*time.Second, 10*time.Millisecond)
	return dc
}

func (e *dcEnv) reconcile(name string) ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(e.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: name}})
	require.NoError(e.t, err)
	return res
}

func (e *dcEnv) get(name string) *downloadv1alpha1.DownloadClient {
	e.t.Helper()
	var dc downloadv1alpha1.DownloadClient
	require.NoError(e.t, e.api.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: name}, &dc))
	return &dc
}

// readyWorkload sets the engine StatefulSet's (or Deployment's) status as
// its controller would: n replicas, all ready. envtest runs no workload
// controller; the write goes under a manager PatchStatus accepts, which owns
// nothing else of the workload's status.
func (e *dcEnv) readyWorkload(dc *downloadv1alpha1.DownloadClient, n int32) {
	e.t.Helper()
	name := engineWorkloadName(dc.Name)
	if dc.Spec.Torrent != nil {
		_, err := k8s.PatchStatus(e.ctx, e.api, k8s.ManagerGrab,
			appsv1ac.StatefulSet(name, testNS).WithStatus(appsv1ac.StatefulSetStatus().WithReplicas(n).WithReadyReplicas(n)))
		require.NoError(e.t, err)
		require.Eventually(e.t, func() bool {
			var s appsv1.StatefulSet
			return e.c.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: name}, &s) == nil && s.Status.ReadyReplicas == n
		}, 10*time.Second, 10*time.Millisecond)
		return
	}
	_, err := k8s.PatchStatus(e.ctx, e.api, k8s.ManagerGrab,
		appsv1ac.Deployment(name, testNS).WithStatus(appsv1ac.DeploymentStatus().WithReplicas(n).WithReadyReplicas(n)))
	require.NoError(e.t, err)
	require.Eventually(e.t, func() bool {
		var d appsv1.Deployment
		return e.c.Get(e.ctx, types.NamespacedName{Namespace: testNS, Name: name}, &d) == nil && d.Status.ReadyReplicas == n
	}, 10*time.Second, 10*time.Millisecond)
}

// record writes ordinal's engine record: fresh, ready, re-attached, with
// room on both directories, unless mutate says otherwise.
func (e *dcEnv) record(dc *downloadv1alpha1.DownloadClient, ordinal int32, mutate func(*schema.EngineRecord)) {
	e.t.Helper()
	item := schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: dc.Namespace, Name: dc.Name, UID: string(dc.UID)}}
	rec := &schema.EngineRecord{
		RecordHeader: schema.RecordHeader{Item: &item, Sub: strconv.Itoa(int(ordinal)), Seq: e.clock().UnixNano(), State: schema.TransferStatePresent},
		Client:       dc.Name, Ordinal: ordinal, Pod: dc.Name + "-engine-" + strconv.Itoa(int(ordinal)), BootID: "boot-1",
		Ready: true, Reattached: true, ScratchFreeBytes: 100 << 30, PublishFreeBytes: 100 << 30,
		ProxyUDP: schema.ProxyUDPAvailable, At: e.clock(),
	}
	if mutate != nil {
		mutate(rec)
	}
	_, err := e.engines.Write(e.ctx, agentrecords.EngineKey(string(dc.UID), ordinal), rec)
	require.NoError(e.t, err)
}

func (e *dcEnv) durables(client string) []string {
	e.t.Helper()
	all, err := e.bus.Subscriptions(e.ctx, cevents.StreamWorkEngine)
	require.NoError(e.t, err)
	var out []string
	for _, d := range all {
		if strings.HasPrefix(d, cevents.EngineConsumerPrefix+cevents.KVKeyToken(client)+"-") {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

func eventReasons(r *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-r.Events:
			f := strings.Fields(ev)
			if len(f) > 1 {
				out = append(out, f[1])
			}
		default:
			return out
		}
	}
}

func condition(dc *downloadv1alpha1.DownloadClient, typ string) *metav1.Condition {
	for i := range dc.Status.Conditions {
		if dc.Status.Conditions[i].Type == typ {
			return &dc.Status.Conditions[i]
		}
	}
	return nil
}

// TestEngineReadyNeedsAFreshEngineRecord: EngineReady is True only when the
// workload's ready replicas equal the desired AND every ordinal has an
// engine record younger than lifecycle.EngineRecordFresh that says ready
// (§6.7): a ready pod whose engine has not re-attached, or has gone quiet,
// takes no grab.
func TestEngineReadyNeedsAFreshEngineRecord(t *testing.T) {
	e := newDCEnv(t)
	dc := e.client(torrentClient("qbt", 2))
	e.reconcile("qbt")
	e.readyWorkload(dc, 2)
	ready := func() bool {
		e.reconcile("qbt")
		return k8s.IsConditionTrue(e.get("qbt").Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady)
	}

	assert.False(t, ready(), "no engine records")
	e.record(dc, 0, nil)
	assert.False(t, ready(), "one of two ordinals reports")
	e.record(dc, 1, func(r *schema.EngineRecord) { r.Ready = false })
	assert.False(t, ready(), "an ordinal not ready (re-attach not reported)")
	e.record(dc, 1, nil)
	assert.True(t, ready(), "both fresh and ready")
	msg := condition(e.get("qbt"), downloadv1alpha1.DownloadClientConditionEngineReady).Message
	assert.Contains(t, msg, "2/2")

	e.advance(lifecycle.EngineRecordFresh + time.Second)
	assert.False(t, ready(), "a record older than EngineRecordFresh")
	assert.Contains(t, condition(e.get("qbt"), downloadv1alpha1.DownloadClientConditionEngineReady).Message, "0 reporting ready")
	e.record(dc, 0, nil)
	e.record(dc, 1, nil)
	assert.True(t, ready())

	e.readyWorkload(dc, 1)
	assert.False(t, ready(), "fresh records but a replica not ready")
}

// TestDiskSpaceOKReadsTheEnginesFreeBytes: DiskSpaceOK compares the
// engines' own free bytes -- the least free of any engine's scratch and
// publish directories -- with minFreeBytes, not a statfs in the manager's
// pod (§6.7); with no record it is False, naming why.
func TestDiskSpaceOKReadsTheEnginesFreeBytes(t *testing.T) {
	e := newDCEnv(t)
	dc := e.client(torrentClient("qbt", 2))
	diskOK := func() (bool, *downloadv1alpha1.DownloadClient) {
		e.reconcile("qbt")
		got := e.get("qbt")
		return k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK), got
	}

	ok, got := diskOK()
	assert.False(t, ok)
	assert.Contains(t, condition(got, downloadv1alpha1.DownloadClientConditionDiskSpaceOK).Message, "no engine has reported")

	e.record(dc, 0, nil)
	e.record(dc, 1, func(r *schema.EngineRecord) { r.ScratchFreeBytes, r.PublishFreeBytes = 200<<30, 2<<30 })
	ok, got = diskOK()
	assert.False(t, ok, "one engine's publish directory is below the floor")
	assert.Equal(t, int64(2<<30), got.Status.FreeBytes, "the least free of any engine's directories")
	assert.Equal(t, ReasonBelowMinFree, condition(got, downloadv1alpha1.DownloadClientConditionDiskSpaceOK).Reason)

	e.record(dc, 1, func(r *schema.EngineRecord) { r.ScratchFreeBytes, r.PublishFreeBytes = 3<<30, 300<<30 })
	ok, _ = diskOK()
	assert.False(t, ok, "a full scratch directory counts too")

	e.record(dc, 1, func(r *schema.EngineRecord) { r.ScratchFreeBytes, r.PublishFreeBytes = 0, 40<<30 })
	ok, got = diskOK()
	assert.True(t, ok, "an engine without scratch reports its publish directory alone")
	assert.Equal(t, int64(40<<30), got.Status.FreeBytes)
}

// TestProxyUDPUnavailableIsAConditionAndOneEvent: an engine record saying
// its proxy refuses UDP sets ProxyUDPUnavailable with one Warning (recorder
// grab-engine) on the transition, not one per reconcile (§7.7, W40).
func TestProxyUDPUnavailableIsAConditionAndOneEvent(t *testing.T) {
	e := newDCEnv(t)
	dc := e.client(torrentClient("qbt", 2))
	proxy := func() bool {
		e.reconcile("qbt")
		return k8s.IsConditionTrue(e.get("qbt").Status.Conditions, downloadv1alpha1.DownloadClientConditionProxyUDPUnavailable)
	}
	e.record(dc, 0, nil)
	e.record(dc, 1, nil)
	assert.False(t, proxy())

	e.record(dc, 1, func(r *schema.EngineRecord) { r.ProxyUDP = schema.ProxyUDPUnavailable })
	assert.True(t, proxy())
	assert.True(t, proxy())
	assert.True(t, proxy())
	got := eventReasons(e.engineRec)
	assert.Equal(t, 1, countOf(got, ReasonProxyUDPUnavailable), "one Warning for the transition: %v", got)

	e.record(dc, 1, nil)
	assert.False(t, proxy())
	e.record(dc, 0, func(r *schema.EngineRecord) { r.ProxyUDP = schema.ProxyUDPUnavailable })
	assert.True(t, proxy())
	assert.Equal(t, 1, countOf(eventReasons(e.engineRec), ReasonProxyUDPUnavailable), "and one for the next")
}

func countOf(xs []string, x string) int {
	n := 0
	for _, s := range xs {
		if s == x {
			n++
		}
	}
	return n
}

// TestEveryRenderedOrdinalHasItsDurable: the manager ensures one engine
// durable per rendered ordinal (§5.1), as events.EngineConsumer declares it;
// a usenet client has one.
func TestEveryRenderedOrdinalHasItsDurable(t *testing.T) {
	e := newDCEnv(t)
	e.client(torrentClient("qbt", 3))
	sab := usenetClient("sab")
	sab.Spec.Usenet.Providers = []downloadv1alpha1.NNTPProvider{{
		Name: "primary", Host: "news.example.com", SecretRef: corev1.LocalObjectReference{Name: "nntp-creds"},
	}}
	e.client(sab)
	e.reconcile("qbt")
	e.reconcile("sab")
	e.reconcile("qbt")

	assert.Equal(t, []string{
		cevents.EngineConsumerName("qbt", 0), cevents.EngineConsumerName("qbt", 1), cevents.EngineConsumerName("qbt", 2),
	}, e.durables("qbt"))
	assert.Equal(t, []string{cevents.EngineConsumerName("sab", 0)}, e.durables("sab"))
	for o := range int32(3) {
		_, err := e.bus.ConsumerState(e.ctx, cevents.StreamWorkEngine, cevents.EngineConsumerName("qbt", o))
		require.NoError(t, err)
	}

	// A command for each ordinal lands on its own durable only.
	for o := range int32(3) {
		_, err := e.bus.Publish(e.ctx, cevents.WorkEngineSubject("qbt", o, "uid-"+strconv.Itoa(int(o))),
			&cevents.Envelope{ID: "m" + strconv.Itoa(int(o)), Schema: "download.EngineCommand.v1", Data: []byte("{}")})
		require.NoError(t, err)
	}
	for o := range int32(3) {
		cs, err := e.bus.ConsumerState(e.ctx, cevents.StreamWorkEngine, cevents.EngineConsumerName("qbt", o))
		require.NoError(t, err)
		assert.Equal(t, uint64(1), cs.Pending, "ordinal %d", o)
	}
}

// TestAScaledDownOrdinalsDurableGoesOnlyWhenNothingIsPinned: a scaled-away
// ordinal's durable is deleted only once no item entry is pinned to its
// engine (an entry pinned there still owes its commands), and a deleted
// client's durables likewise.
func TestAScaledDownOrdinalsDurableGoesOnlyWhenNothingIsPinned(t *testing.T) {
	e := newDCEnv(t)
	dc := e.client(torrentClient("qbt", 3))
	e.reconcile("qbt")
	require.Len(t, e.durables("qbt"), 3)

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-1995", Namespace: testNS},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, e.c.Create(e.ctx, movie))
	var pinned []string
	pin := func(engines ...string) {
		t.Helper()
		var entries []catalogv1alpha1.DownloadEntry
		for i, eng := range engines {
			en := itemtest.Entry("heat-1995-"+strconv.Itoa(i), commonv1alpha1.DownloadPhaseRemoving)
			en.Client, en.Engine = "qbt", eng
			entries = append(entries, en)
		}
		require.NoError(t, itemtest.SeedEntries(e.ctx, e.api, movie, entries...))
		was := pinned
		pinned = engines
		require.Eventually(t, func() bool {
			for _, eng := range append(append([]string(nil), was...), engines...) {
				p, err := dlindex.PinnedTo(e.ctx, e.c, testNS, eng)
				if err != nil || (len(p) > 0) != slices.Contains(engines, eng) {
					return false
				}
			}
			return true
		}, 10*time.Second, 10*time.Millisecond, "the cache's index sees the pins")
	}
	pin("qbt-2")

	dc = e.get("qbt")
	dc.Spec.Replicas = 1
	require.NoError(t, e.api.Update(e.ctx, dc))
	require.Eventually(t, func() bool {
		var got downloadv1alpha1.DownloadClient
		return e.c.Get(e.ctx, client.ObjectKeyFromObject(dc), &got) == nil && got.Spec.Replicas == 1
	}, 10*time.Second, 10*time.Millisecond)
	e.reconcile("qbt")
	assert.Equal(t, []string{cevents.EngineConsumerName("qbt", 0), cevents.EngineConsumerName("qbt", 2)}, e.durables("qbt"),
		"ordinal 1 is gone; ordinal 2 stays while an entry is pinned to qbt-2")

	pin()
	e.reconcile("qbt")
	assert.Equal(t, []string{cevents.EngineConsumerName("qbt", 0)}, e.durables("qbt"))

	// The client deleted: its last durable stays while pinned, then goes.
	pin("qbt-0")
	require.NoError(t, e.api.Delete(e.ctx, dc))
	require.Eventually(t, func() bool {
		return e.c.Get(e.ctx, client.ObjectKeyFromObject(dc), &downloadv1alpha1.DownloadClient{}) != nil
	}, 10*time.Second, 10*time.Millisecond)
	res := e.reconcile("qbt")
	assert.Equal(t, time.Minute, res.RequeueAfter, "come back while an entry is pinned")
	assert.Equal(t, []string{cevents.EngineConsumerName("qbt", 0)}, e.durables("qbt"))
	pin()
	e.reconcile("qbt")
	assert.Empty(t, e.durables("qbt"))
}
