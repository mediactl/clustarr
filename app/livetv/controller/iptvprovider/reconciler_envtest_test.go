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

package iptvprovider_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/controller/iptvprovider"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var cfg *rest.Config

func TestMain(m *testing.M) {
	var stop func()
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
		var err error
		if cfg, err = env.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start envtest:", err)
			os.Exit(1)
		}
		stop = func() { _ = env.Stop() }
	}
	code := m.Run()
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// fakeTuner records what the controller hands the tuner.
type fakeTuner struct {
	mu      sync.Mutex
	applied map[types.NamespacedName]tuner.Config
}

func (f *fakeTuner) Apply(c tuner.Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied[c.Key] = c
}

func (f *fakeTuner) Remove(key types.NamespacedName) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.applied, key)
}

func (*fakeTuner) Snapshot(types.NamespacedName) (tuner.Snapshot, bool) {
	return tuner.Snapshot{}, false
}

func (f *fakeTuner) last(key types.NamespacedName) (tuner.Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.applied[key]
	return c, ok
}

type harness struct {
	c      client.Client
	tuner  *fakeTuner
	events chan event.GenericEvent
	ns     string
	ctx    context.Context
	now    atomic.Pointer[time.Time]
	// plans are the stored lineup plans by provider name, which the
	// reconciler's PlanReader reads (storePlan).
	plans sync.Map
}

// readPlan is the reconciler's PlanReader: the plan a test stored for the
// provider, none when it stored none.
func (h *harness) readPlan(_ context.Context, key types.NamespacedName) (*livetvstore.LineupPlan, bool, error) {
	v, ok := h.plans.Load(key.Name)
	if !ok {
		return nil, false, nil
	}
	return v.(*livetvstore.LineupPlan), true, nil
}

// storePlan stores lp as provider's lineup plan, as the lineup controller
// does; lp is not changed afterwards.
func (h *harness) storePlan(provider string, lp *livetvstore.LineupPlan) { h.plans.Store(provider, lp) }

var nsSeq atomic.Int32

func setup(t *testing.T) *harness {
	t.Helper()
	if cfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	h := &harness{tuner: &fakeTuner{applied: map[types.NamespacedName]tuner.Config{}}, events: make(chan event.GenericEvent, 64), ns: fmt.Sprintf("media-%d", nsSeq.Add(1))}
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	h.now.Store(&start)
	scheme := k8s.MustNewScheme()
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, direct.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}}))
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := &iptvprovider.Reconciler{
		Client: mgr.GetClient(), SecretReader: direct, Tuner: h.tuner,
		Address:    "livetv.clustarr-system.svc.cluster.local:80",
		PlanReader: h.readPlan,
		Events:     h.events, Now: func() time.Time { return *h.now.Load() },
	}
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	h.c, h.ctx = direct, ctx
	t.Cleanup(func() {
		// Providers are compared cluster-wide: none outlives its test.
		_ = direct.DeleteAllOf(context.Background(), &clustarrv1.IPTVProvider{}, client.InNamespace(h.ns))
		require.Eventually(t, func() bool {
			var l clustarrv1.IPTVProviderList
			return direct.List(context.Background(), &l, client.InNamespace(h.ns)) == nil && len(l.Items) == 0
		}, 10*time.Second, 50*time.Millisecond)
		cancel()
		<-done
	})
	return h
}

func (h *harness) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: h.ns, Name: name}
}

func (h *harness) provider(t *testing.T, name string, mutate ...func(*clustarrv1.IPTVProvider)) {
	t.Helper()
	if err := h.c.Create(h.ctx, credentials(h.ns, name, map[string][]byte{"username": []byte("user"), "password": []byte("pass")})); !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns}, Spec: clustarrv1.IPTVProviderSpec{
		Server:         "p.example",
		CredentialsRef: corev1.LocalObjectReference{Name: name + "-credentials"},
		Tuners:         ptr.To(int32(2)),
		Filters:        []clustarrv1.ChannelFilter{{Name: "all", Type: clustarrv1.FilterCustom, Match: "a"}},
	}}
	for _, m := range mutate {
		m(p)
	}
	require.NoError(t, h.c.Create(h.ctx, p))
}

func (h *harness) channels(t *testing.T, provider string, numbers ...int) {
	t.Helper()
	for _, n := range numbers {
		require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", provider, n), Namespace: h.ns},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: provider, Key: fmt.Sprintf("t:c%d", n), Number: fmt.Sprint(n), Active: true},
		}))
	}
}

func (h *harness) get(t *testing.T, name string) *clustarrv1.IPTVProvider {
	t.Helper()
	var p clustarrv1.IPTVProvider
	require.NoError(t, h.c.Get(h.ctx, h.key(name), &p))
	return &p
}

// until waits for name's status to satisfy ok, and returns it.
func (h *harness) until(t *testing.T, name string, ok func(*clustarrv1.IPTVProvider) bool, why string) *clustarrv1.IPTVProvider {
	t.Helper()
	var p *clustarrv1.IPTVProvider
	require.Eventually(t, func() bool { p = h.get(t, name); return ok(p) }, 15*time.Second, 50*time.Millisecond, why)
	return p
}

// credentials is provider name's credentials Secret holding data.
func credentials(ns, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-credentials", Namespace: ns}, Data: data}
}

func channelsIn(p *clustarrv1.IPTVProvider) int {
	n := 0
	for _, b := range p.Status.Blocks {
		n += int(b.Channels)
	}
	return n
}

func hasChannels(n int) func(*clustarrv1.IPTVProvider) bool {
	return func(p *clustarrv1.IPTVProvider) bool { return channelsIn(p) == n }
}

func ready(p *clustarrv1.IPTVProvider) *metav1.Condition {
	return meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionReady)
}

func uris(p *clustarrv1.IPTVProvider) []string {
	var out []string
	for _, b := range p.Status.Blocks {
		out = append(out, b.URI)
	}
	return out
}

func run(from, to int) []int {
	var out []int
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

// small packs three channels a block (a first packing fills to 75% of 4).
func small(p *clustarrv1.IPTVProvider) { p.Spec.DVR.MaxChannels = ptr.To(int32(4)) }

func TestAProviderPublishesItsBlocksAddressAndTunerConfig(t *testing.T) {
	h := setup(t)
	// The channels first, so the provider's first packing sees them all.
	h.channels(t, "news", run(1000, 1006)...)
	h.provider(t, "news", small)
	p := h.until(t, "news", hasChannels(7), "seven channels in blocks")
	assert.Equal(t, "livetv.clustarr-system.svc.cluster.local:80", p.Status.Address)
	require.Len(t, p.Status.Blocks, 3)
	for i, b := range p.Status.Blocks {
		start := fmt.Sprint(1000 + 3*i)
		assert.Equal(t, start, b.Start)
		assert.Equal(t, hdhr.BlockDeviceID(h.ns, "news", start), b.DeviceID)
		assert.Equal(t, "Clustarr news "+start+"+", b.FriendlyName)
		assert.Equal(t, iptvprovider.BlockGuidePath, b.GuidePath)
		assert.Equal(t, tuner.DeviceEndpoint(b.DeviceID), b.Path, "the device's path")
		assert.Equal(t, "http://"+p.Status.Address+b.Path, b.URI, "the URI Plex is given")
		assert.NotZero(t, b.MapBytes)
	}
	assert.Equal(t, []string{"1000", "1002"}, []string{p.Status.Blocks[0].First, p.Status.Blocks[0].Last})
	c, ok := h.tuner.last(h.key("news"))
	require.True(t, ok)
	require.Len(t, c.Blocks, 3)
	assert.Equal(t, []string{"1006"}, c.Blocks[2].Numbers)
	assert.Equal(t, p.Status.Blocks[2].DeviceID, c.Blocks[2].DeviceID)
}

func TestAnAddedChannelMovesNoOtherChannelAndURIsAndIDsHold(t *testing.T) {
	h := setup(t)
	var nums []int
	for n := 1000; n < 1018; n += 2 { // 9 channels, room between them
		nums = append(nums, n)
	}
	h.channels(t, "news", nums...)
	h.provider(t, "news", small)
	before := h.until(t, "news", hasChannels(9), "three blocks")
	require.Len(t, before.Status.Blocks, 3)

	t.Run("a spec edit changes no URI", func(t *testing.T) {
		p := h.get(t, "news")
		p.Spec.Tuners = ptr.To(int32(3))
		require.NoError(t, h.c.Update(h.ctx, p))
		require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return c.Tuners == 3 }, 15*time.Second, 50*time.Millisecond)
		assert.Equal(t, uris(before), uris(h.get(t, "news")))
	})

	h.channels(t, "news", 1007) // inside the middle block, 1006-1010
	after := h.until(t, "news", hasChannels(10), "the added channel placed")
	require.Len(t, after.Status.Blocks, 3, "the middle block takes it without a split")
	for i := range 3 {
		b, a := before.Status.Blocks[i], after.Status.Blocks[i]
		assert.Equal(t, []any{b.Start, b.DeviceID, b.URI}, []any{a.Start, a.DeviceID, a.URI}, "block %d keeps its start, device and URI", i)
		want := b.Channels
		if i == 1 {
			want++
		}
		assert.Equal(t, want, a.Channels, "only the middle block changes")
	}

	h.channels(t, "news", 1009, 1011) // the middle block now holds 5 > 4: it splits
	split := h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool { return len(p.Status.Blocks) == 4 }, "a split")
	assert.Equal(t, []any{before.Status.Blocks[0].Start, before.Status.Blocks[0].URI}, []any{split.Status.Blocks[0].Start, split.Status.Blocks[0].URI})
	assert.Equal(t, []any{before.Status.Blocks[1].Start, before.Status.Blocks[1].URI, before.Status.Blocks[1].DeviceID},
		[]any{split.Status.Blocks[1].Start, split.Status.Blocks[1].URI, split.Status.Blocks[1].DeviceID}, "the lower half stays")
	assert.Equal(t, []any{before.Status.Blocks[2].Start, before.Status.Blocks[2].URI}, []any{split.Status.Blocks[3].Start, split.Status.Blocks[3].URI})
	assert.NotContains(t, uris(before), split.Status.Blocks[2].URI, "the new block gets a URI of its own")
}

func TestAnExplicitDeviceIDPinsTheFirstBlockAndADuplicateIsRefused(t *testing.T) {
	h := setup(t)
	h.provider(t, "a", func(p *clustarrv1.IPTVProvider) { p.Spec.Device.ID = "CAFE0001" })
	h.channels(t, "a", 1000, 1001)
	a := h.until(t, "a", hasChannels(2), "a's block")
	assert.Equal(t, "CAFE0001", a.Status.Blocks[0].DeviceID)

	h.provider(t, "b", func(p *clustarrv1.IPTVProvider) { p.Spec.Device.ID = "CAFE0001" })
	h.channels(t, "b", 2000)
	b := h.until(t, "b", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonDuplicateDeviceID
	}, "b's device ID is a's")
	assert.Equal(t, metav1.ConditionFalse, ready(b).Status)
	assert.Empty(t, b.Status.Blocks, "b gets no blocks")
	_, applied := h.tuner.last(h.key("b"))
	assert.True(t, applied, "the tuner config carries the kept (no) blocks")
	assert.Equal(t, "CAFE0001", h.get(t, "a").Status.Blocks[0].DeviceID)
}

func TestAnOverlapKeepsTheLastGoodBlocks(t *testing.T) {
	h := setup(t)
	h.provider(t, "a")
	h.channels(t, "a", run(1000, 1009)...)
	a := h.until(t, "a", hasChannels(10), "a's blocks")
	h.provider(t, "b")
	h.channels(t, "b", run(2000, 2009)...)
	b := h.until(t, "b", hasChannels(10), "b's blocks")

	h.channels(t, "b", 1005) // inside a's span
	got := h.until(t, "b", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonNumberRangeOverlap
	}, "b's span would overlap a's")
	assert.Contains(t, ready(got).Message, "the last good blocks are kept")
	assert.Equal(t, b.Status.Blocks, got.Status.Blocks, "b's blocks, and so its DVRs, as they were")
	assert.Equal(t, a.Status.Blocks, h.get(t, "a").Status.Blocks)
	c, _ := h.tuner.last(h.key("b"))
	require.Len(t, c.Blocks, 1)
	assert.Contains(t, c.Blocks[0].Numbers, "2000")
}

func TestTooManyChannelsKeepsTheLastGoodBlocks(t *testing.T) {
	h := setup(t)
	h.channels(t, "news", run(1000, 1095)...) // 32 blocks of 3
	h.provider(t, "news", small)
	good := h.until(t, "news", hasChannels(96), "32 blocks")
	require.Len(t, good.Status.Blocks, 32)

	h.channels(t, "news", run(1096, 1101)...)
	got := h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonTooManyChannels
	}, "more than 32 blocks")
	// The channels arrive one at a time, so the last good blocks may have
	// taken one more channel before the overflow; their identities hold.
	identity := func(p *clustarrv1.IPTVProvider) []string {
		var out []string
		for _, b := range p.Status.Blocks {
			out = append(out, b.Start+"/"+b.DeviceID+"/"+b.URI)
		}
		return out
	}
	assert.Equal(t, identity(good), identity(got), "32 blocks, their starts, devices and URIs kept")
	c, _ := h.tuner.last(h.key("news"))
	assert.Len(t, c.Blocks, 32, "the tuner keeps serving them")
}

func TestABlocksOwnerIsItsLiveLeasesHolder(t *testing.T) {
	h := setup(t)
	h.provider(t, "news")
	h.channels(t, "news", 1000)
	p := h.until(t, "news", hasChannels(1), "a block")
	id := p.Status.Blocks[0].DeviceID
	now := *h.now.Load()
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: iptvprovider.LeaseName(id), Namespace: h.ns, Labels: map[string]string{iptvprovider.LabelLeaseProvider: "news"}},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: ptr.To("machine-a"), LeaseDurationSeconds: ptr.To(int32(600)),
			RenewTime: &metav1.MicroTime{Time: now.Add(-time.Minute)},
		},
	}
	require.NoError(t, h.c.Create(h.ctx, lease))
	h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool { return p.Status.Blocks[0].Owner == "machine-a" }, "a live Lease's holder")

	later := now.Add(time.Hour)
	h.now.Store(&later)
	lease.Spec.LeaseDurationSeconds = ptr.To(int32(601)) // any change wakes the provider
	require.NoError(t, h.c.Update(h.ctx, lease))
	h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool { return p.Status.Blocks[0].Owner == "" }, "an expired Lease gives no owner")
}

func TestOnlyTheLiveTVManagerWritesTheProvidersStatus(t *testing.T) {
	h := setup(t)
	h.provider(t, "news")
	h.channels(t, "news", 1000)
	p := h.until(t, "news", hasChannels(1), "a block")
	var managers []string
	for _, mf := range p.ManagedFields {
		if mf.Subresource == "status" {
			managers = append(managers, mf.Manager)
		}
	}
	assert.Equal(t, []string{string(k8s.ManagerLiveTV)}, managers)
	assert.False(t, strings.Contains(fmt.Sprint(p.Status), "pass"), "no credential in status")
}

func TestChannelsReachTheTunerThroughTheIndex(t *testing.T) {
	h := setup(t)
	h.provider(t, "news")
	h.provider(t, "other", func(p *clustarrv1.IPTVProvider) { p.Spec.ChannelNumberStart = ptr.To(int32(5000)) })
	mk := func(name, provider, number string, epg *clustarrv1.ChannelEPG) {
		require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: provider, Key: "t:" + name, Number: number, Active: true, EPG: epg},
		}))
	}
	mk("bbc1", "news", "1001", &clustarrv1.ChannelEPG{ChannelID: "bbc1.uk"})
	mk("filler", "news", "1002", &clustarrv1.ChannelEPG{Dummy: true, DummyMinutes: ptr.To(int32(30))})
	mk("cnn", "other", "5001", nil)
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return len(c.Channels) == 2 }, 15*time.Second, 50*time.Millisecond)
	c, _ := h.tuner.last(h.key("news"))
	byObject := map[string]tuner.ChannelConfig{}
	for _, ch := range c.Channels {
		byObject[ch.Object] = ch
	}
	assert.Equal(t, "t:bbc1", byObject["bbc1"].Key)
	assert.Equal(t, "1001", byObject["bbc1"].Number)
	assert.True(t, byObject["bbc1"].Active)
	require.NotNil(t, byObject["bbc1"].Map)
	assert.Equal(t, "bbc1.uk", byObject["bbc1"].Map.ChannelID)
	assert.Equal(t, tuner.GuideName, byObject["bbc1"].Map.Guide)
	assert.Nil(t, byObject["filler"].Map)
	assert.Equal(t, 30, byObject["filler"].DummyMinutes)
	assert.NotContains(t, byObject, "cnn", "another provider's channel")

	mk("itv", "news", "1003", nil)
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return len(c.Channels) == 3 }, 15*time.Second, 50*time.Millisecond)
}

func TestADisabledProviderIsRemovedFromTheTunerAndReadsDisabled(t *testing.T) {
	h := setup(t)
	h.provider(t, "news")
	h.channels(t, "news", 1000)
	h.until(t, "news", hasChannels(1), "a block")
	_, ok := h.tuner.last(h.key("news"))
	require.True(t, ok)

	p := h.get(t, "news")
	p.Spec.Enabled = ptr.To(false)
	require.NoError(t, h.c.Update(h.ctx, p))
	got := h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonDisabled
	}, "Ready=False Disabled")
	assert.Equal(t, metav1.ConditionFalse, ready(got).Status)
	_, ok = h.tuner.last(h.key("news"))
	assert.False(t, ok, "the tuner no longer serves it")
	assert.Len(t, got.Status.Blocks, 1, "its blocks, and so its DVRs, are kept")
}

func TestTheProviderWhoseEditOverlapsIsRefusedWhicheverIsOlder(t *testing.T) {
	// Older a gains a channel inside younger b's span: a's edit is the one
	// refused. b, which nobody touched, keeps its blocks and stays Ready,
	// and no number sits in two DVRs.
	h := setup(t)
	h.provider(t, "a")
	h.channels(t, "a", run(1000, 1009)...)
	a := h.until(t, "a", hasChannels(10), "a's blocks")
	h.provider(t, "b")
	h.channels(t, "b", run(2000, 2009)...)
	b := h.until(t, "b", hasChannels(10), "b's blocks")

	h.channels(t, "a", 2005) // inside b's span
	got := h.until(t, "a", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonNumberRangeOverlap
	}, "a's span would overlap b's")
	assert.Equal(t, a.Status.Blocks, got.Status.Blocks, "a's blocks as they were")

	h.channels(t, "b", 2010) // b reconciles against a's published span, which a's edit left
	after := h.until(t, "b", hasChannels(11), "b takes its own new channel")
	assert.NotEqual(t, clustarrv1.ReasonNumberRangeOverlap, ready(after).Reason, "b is not the one refused")
	assert.Equal(t, b.Status.Blocks[0].URI, after.Status.Blocks[0].URI)
}

func TestTheCredentialsTrailingNewlinesAreTrimmed(t *testing.T) {
	// kubectl create secret --from-file and a YAML "|" block both end the
	// value in a newline; an untrimmed credential never logs in.
	h := setup(t)
	require.NoError(t, h.c.Create(h.ctx, credentials(h.ns, "polutv", map[string][]byte{"username": []byte("a\n"), "password": []byte(" b\n")})))
	h.provider(t, "polutv", func(p *clustarrv1.IPTVProvider) { p.Spec.Server = "http://p.example:8080/" })
	require.Eventually(t, func() bool { c, ok := h.tuner.last(h.key("polutv")); return ok && c.Username == "a" }, 10*time.Second, 20*time.Millisecond)
	c, _ := h.tuner.last(h.key("polutv"))
	assert.Equal(t, "b", c.Password)
	assert.Equal(t, "http://p.example:8080", c.Server)
	assert.Equal(t, "http://p.example:8080/get.php?username=a&password=b&output=ts&type=m3u_plus", c.PlaylistURL())
	assert.Equal(t, "http://p.example:8080/xmltv.php?username=a&password=b", c.GuideSource())
}

func TestAMissingCredentialsKeyReadsInvalid(t *testing.T) {
	h := setup(t)
	// A value no status could hold by chance: "a" was in every address.
	require.NoError(t, h.c.Create(h.ctx, credentials(h.ns, "news", map[string][]byte{"username": []byte("usr-7f3c9d")})))
	h.provider(t, "news")
	got := h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool {
		c := ready(p)
		return c != nil && c.Reason == clustarrv1.ReasonSecretKeyNotFound
	}, "Ready=False SecretKeyNotFound")
	assert.NotContains(t, fmt.Sprint(got.Status), "usr-7f3c9d", "no credential in status")
}

// TestACategoryLineupPublishesABlockPerCategory: with spec.lineup enabled,
// its plan stored, and every channel carrying it (status.lineup as the
// lineup controller writes it, the planned spec.number and spec.active),
// each category holding an active channel is a block of its own, started at
// its base+1, under the device named for its category (the custom guides
// design §5.5); status.categories lists the plan's categories in its order
// with their bases, active channels and devices, and LineupApplied is True.
// The tuner is handed the blocks with their categories and numbers, and
// each channel with its category and its planned name.
func TestACategoryLineupPublishesABlockPerCategory(t *testing.T) {
	h := setup(t)
	planned := []lineup.Assignment{
		{Key: "t:cnn", Name: "CNN", Category: "News", Rule: "News[1]", Number: "1001", Base: 1000, Index: 0},
		{Key: "t:msnbc", Name: "MSNBC", Category: "News", Rule: "News[1]", Number: "1002", Base: 1000, Index: 1},
		{Key: "t:espn", Name: "ESPN", Category: "Sports", Rule: "Sports[0]", Number: "2001", Base: 2000, Index: 0},
	}
	for _, a := range planned {
		name := "news-" + strings.TrimPrefix(a.Key, "t:")
		require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns, Labels: map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginLineup}},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: "news", Key: a.Key, Number: a.Number, Active: true},
		}))
		_, err := k8s.PatchStatus(h.ctx, h.c, k8s.ManagerLiveTVLineup, clustarrac.IPTVChannel(name, h.ns).WithStatus(
			clustarrac.IPTVChannelStatus().WithLineup(clustarrac.ChannelLineupStatus().
				WithName(a.Name).WithCategory(a.Category).WithRule(a.Rule).WithNumber(a.Number))))
		require.NoError(t, err)
	}
	h.storePlan("news", &livetvstore.LineupPlan{
		PlannedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		Plan: lineup.Plan{
			Channels: planned,
			// In rule order: Sports's rule comes first.
			Categories: []lineup.Category{{Name: "Sports", Base: 2000, Channels: 1}, {Name: "News", Base: 1000, Channels: 2}},
		},
	})
	h.provider(t, "news", func(p *clustarrv1.IPTVProvider) {
		p.Spec.Lineup = &clustarrv1.LineupSpec{Enabled: true, Categories: []clustarrv1.LineupCategory{
			{Name: "Sports", Base: ptr.To(int32(2000)), Names: []string{"^ESPN"}},
			{Name: "News", Base: ptr.To(int32(1000)), Names: []string{"^CNN", "^MSNBC"}},
		}}
	})

	p := h.until(t, "news", func(p *clustarrv1.IPTVProvider) bool {
		c := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionLineupApplied)
		return len(p.Status.Blocks) == 2 && len(p.Status.Categories) == 2 && c != nil && c.Status == metav1.ConditionTrue
	}, "two category blocks and the lineup applied")
	newsID := hdhr.BlockDeviceID(h.ns, "news", "cat/News")
	sportsID := hdhr.BlockDeviceID(h.ns, "news", "cat/Sports")
	type block struct {
		Start, Category, DeviceID, FriendlyName, First, Last string
		Channels                                             int32
	}
	var got []block
	for _, b := range p.Status.Blocks {
		got = append(got, block{b.Start, b.Category, b.DeviceID, b.FriendlyName, b.First, b.Last, b.Channels})
		assert.Equal(t, tuner.DeviceEndpoint(b.DeviceID), b.Path)
		assert.Equal(t, "http://"+p.Status.Address+b.Path, b.URI, "the URI Plex is given")
		assert.Equal(t, iptvprovider.BlockGuidePath, b.GuidePath, "a category's device serves its XMLTV guide")
		assert.Empty(t, b.Lineup)
	}
	assert.Equal(t, []block{
		{"1001", "News", newsID, "Clustarr news News", "1001", "1002", 2},
		{"2001", "Sports", sportsID, "Clustarr news Sports", "2001", "2001", 1},
	}, got, "a block per category, in start order, its device the category's")
	assert.Equal(t, []clustarrv1.CategoryStatus{
		{Name: "Sports", Base: 2000, Channels: 1, DeviceID: sportsID},
		{Name: "News", Base: 1000, Channels: 2, DeviceID: newsID},
	}, p.Status.Categories, "the plan's categories, in its order")
	applied := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionLineupApplied)
	require.NotNil(t, applied)
	assert.Equal(t, clustarrv1.ReasonApplied, applied.Reason)
	assert.Equal(t, "3 channels in 2 categories", applied.Message)

	c, ok := h.tuner.last(h.key("news"))
	require.True(t, ok)
	type tb struct {
		Start, DeviceID, Category string
		Numbers                   []string
	}
	var blocks []tb
	for _, b := range c.Blocks {
		blocks = append(blocks, tb{b.Start, b.DeviceID, b.Category, b.Numbers})
	}
	assert.Equal(t, []tb{{"1001", newsID, "News", []string{"1001", "1002"}}, {"2001", sportsID, "Sports", []string{"2001"}}}, blocks)
	byKey := map[string]tuner.ChannelConfig{}
	for _, ch := range c.Channels {
		byKey[ch.Key] = ch
	}
	for _, a := range planned {
		ch := byKey[a.Key]
		assert.Equal(t, []string{a.Number, a.Category, a.Name, a.Category}, []string{ch.Number, ch.Category, ch.Name, ch.Group},
			"%s: its number, category, the planned name, and its category as the guide's class", a.Key)
	}
}
