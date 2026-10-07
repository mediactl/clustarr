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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/controller/iptvprovider"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var (
	cfg  *rest.Config
	stop func()
)

func TestMain(m *testing.M) {
	code := 0
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
		var err error
		if cfg, err = env.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start envtest:", err)
			os.Exit(1)
		}
		stop = func() { _ = env.Stop() }
	}
	code = m.Run()
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// fakeTuner records what the controller hands the tuner and answers a
// settable snapshot.
type fakeTuner struct {
	mu      sync.Mutex
	applied map[types.NamespacedName]tuner.Config
	applies atomic.Int32
	removed map[types.NamespacedName]int
	snaps   map[types.NamespacedName]tuner.Snapshot
}

func newFakeTuner() *fakeTuner {
	return &fakeTuner{applied: map[types.NamespacedName]tuner.Config{}, removed: map[types.NamespacedName]int{}, snaps: map[types.NamespacedName]tuner.Snapshot{}}
}

func (f *fakeTuner) Apply(c tuner.Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied[c.Key] = c
	f.applies.Add(1)
}

func (f *fakeTuner) Remove(key types.NamespacedName) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[key]++
	delete(f.applied, key)
}

func (f *fakeTuner) Snapshot(key types.NamespacedName) (tuner.Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.snaps[key]
	return s, ok
}

func (f *fakeTuner) set(key types.NamespacedName, s *tuner.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s == nil {
		delete(f.snaps, key)
		return
	}
	f.snaps[key] = *s
}

func (f *fakeTuner) last(key types.NamespacedName) (tuner.Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.applied[key]
	return c, ok
}

func (f *fakeTuner) removals(key types.NamespacedName) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed[key]
}

type harness struct {
	c      client.Client
	tuner  *fakeTuner
	events chan event.GenericEvent
	mgrNS  string
	ns     string
	ctx    context.Context
}

var nsSeq atomic.Int32

func setup(t *testing.T) *harness {
	t.Helper()
	if cfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	n := nsSeq.Add(1)
	h := &harness{
		tuner: newFakeTuner(), events: make(chan event.GenericEvent, 16),
		mgrNS: fmt.Sprintf("clustarr-system-%d", n), ns: fmt.Sprintf("media-%d", n),
	}
	scheme := k8s.MustNewScheme()
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for _, ns := range []string{h.mgrNS, h.ns} {
		require.NoError(t, direct.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := &iptvprovider.Reconciler{
		Client: mgr.GetClient(), SecretReader: direct, Tuner: h.tuner, Namespace: h.mgrNS,
		Selector: map[string]string{"app.kubernetes.io/component": "manager"}, TargetPort: 5004, Events: h.events,
	}
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.c, h.ctx = direct, ctx
	return h
}

func (h *harness) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: h.ns, Name: name}
}

func (h *harness) secret(t *testing.T, name, url string) {
	require.NoError(t, h.c.Create(h.ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		StringData: map[string]string{"playlist-url": url},
	}))
}

func (h *harness) provider(t *testing.T, name string, mutate ...func(*clustarrv1.IPTVProvider)) *clustarrv1.IPTVProvider {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns}, Spec: clustarrv1.IPTVProviderSpec{
		Tuners:   2,
		Playlist: clustarrv1.PlaylistSource{URLFrom: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-livetv"}, Key: "playlist-url"}},
		Guides:   []clustarrv1.XMLTVSource{{Name: "main", URL: "http://epg.example/guide.xml"}},
		Filters:  []clustarrv1.ChannelFilter{{Name: "all", Type: clustarrv1.FilterCustom, Match: "a"}},
	}}
	for _, m := range mutate {
		m(p)
	}
	require.NoError(t, h.c.Create(h.ctx, p))
	return p
}

func (h *harness) get(t *testing.T, name string) *clustarrv1.IPTVProvider {
	var p clustarrv1.IPTVProvider
	require.NoError(t, h.c.Get(h.ctx, h.key(name), &p))
	return &p
}

func (h *harness) service(name string) (*corev1.Service, error) {
	var svc corev1.Service
	err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.mgrNS, Name: iptvprovider.ServiceName(h.ns, name)}, &svc)
	return &svc, err
}

func (h *harness) wake(name string) {
	h.events <- event.GenericEvent{Object: &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns}}}
}

const playlistURL = "http://p.example/get.php?username=u&password=p"

func TestAProviderGetsAServiceInTheManagersNamespaceAndAnAddress(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	h.provider(t, "news")
	var svc *corev1.Service
	require.Eventually(t, func() bool { s, err := h.service("news"); svc = s; return err == nil }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, map[string]string{"app.kubernetes.io/component": "manager"}, svc.Spec.Selector)
	require.Len(t, svc.Spec.Ports, 1)
	assert.Equal(t, int32(80), svc.Spec.Ports[0].Port)
	assert.Equal(t, int32(5004), svc.Spec.Ports[0].TargetPort.IntVal)
	assert.Equal(t, h.ns, svc.Labels[iptvprovider.LabelProviderNamespace])
	assert.Equal(t, "news", svc.Labels[iptvprovider.LabelProviderName])
	require.Eventually(t, func() bool { return h.get(t, "news").Status.Address != "" }, 10*time.Second, 50*time.Millisecond)
	p := h.get(t, "news")
	assert.Equal(t, svc.Spec.ClusterIP, p.Status.Address)
	assert.Equal(t, hdhr.DeviceID(h.ns, "news"), p.Status.DeviceID)
	assert.Equal(t, "http://"+svc.Spec.ClusterIP+"/livetv/"+h.ns+"/news/xmltv.xml", p.Status.GuideURL)
	assert.Contains(t, p.Finalizers, iptvprovider.Finalizer)
	c, ok := h.tuner.last(h.key("news"))
	require.True(t, ok)
	assert.Equal(t, playlistURL, c.PlaylistURL, "the Secret's value reaches the tuner")
	assert.Equal(t, svc.Spec.ClusterIP, c.Address)
	assert.Equal(t, "Clustarr news", c.FriendlyName)
	assert.Equal(t, 2, c.Tuners)
	assert.Equal(t, "XEPG", c.EPGSource)
	assert.Equal(t, []tuner.GuideConfig{{Name: "main", URL: "http://epg.example/guide.xml", Refresh: 12 * time.Hour}}, c.Guides)
	assert.Equal(t, int(clustarrv1.DefaultStreamBuffer), c.Buffer)
}

func TestAProviderSpecEditKeepsTheServicesClusterIP(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	h.provider(t, "news")
	var before *corev1.Service
	require.Eventually(t, func() bool { s, err := h.service("news"); before = s; return err == nil }, 10*time.Second, 50*time.Millisecond)
	p := h.get(t, "news")
	p.Spec.Tuners = 3
	require.NoError(t, h.c.Update(h.ctx, p))
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return c.Tuners == 3 }, 10*time.Second, 50*time.Millisecond)
	after, err := h.service("news")
	require.NoError(t, err)
	assert.Equal(t, before.UID, after.UID)
	assert.Equal(t, before.Spec.ClusterIP, after.Spec.ClusterIP)
}

func TestDeletingAProviderDeletesItsServiceAndReleasesTheTuner(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	p := h.provider(t, "news")
	require.Eventually(t, func() bool { _, err := h.service("news"); return err == nil }, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool { return len(h.get(t, "news").Finalizers) > 0 }, 10*time.Second, 50*time.Millisecond)
	require.NoError(t, h.c.Delete(h.ctx, p))
	require.Eventually(t, func() bool { _, err := h.service("news"); return apierrors.IsNotFound(err) }, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		err := h.c.Get(h.ctx, h.key("news"), &clustarrv1.IPTVProvider{})
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 50*time.Millisecond)
	assert.Positive(t, h.tuner.removals(h.key("news")))
}

func TestAProviderWithAMissingSecretIsInvalid(t *testing.T) {
	h := setup(t)
	h.provider(t, "news")
	var p *clustarrv1.IPTVProvider
	require.Eventually(t, func() bool {
		p = h.get(t, "news")
		return meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionValid) != nil
	}, 10*time.Second, 50*time.Millisecond)
	valid := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionValid)
	assert.Equal(t, metav1.ConditionFalse, valid.Status)
	assert.Equal(t, clustarrv1.ReasonSecretNotFound, valid.Reason)
	assert.Contains(t, valid.Message, "news-livetv")
	assert.Equal(t, metav1.ConditionFalse, meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionReady).Status)
	_, applied := h.tuner.last(h.key("news"))
	assert.False(t, applied)
}

func snapshot() *tuner.Snapshot {
	return &tuner.Snapshot{
		PlaylistFetched: true,
		Playlist:        tuner.PlaylistState{Entries: 120, Groups: 7, Candidates: 40, FetchedAt: time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC), Hash: "ph"},
		Guides:          []tuner.GuideState{{Name: "main", Channels: 300, Programmes: 9000, FetchedAt: time.Date(2026, 10, 7, 1, 1, 0, 0, time.UTC)}},
		Lineup:          tuner.LineupState{Active: 12, Unmapped: 2, Missing: 1, Hash: "lh"},
		GuideHash:       "gh",
		TunersInUse:     1,
	}
}

func steady(t *testing.T, h *harness) {
	h.secret(t, "news-livetv", playlistURL)
	h.provider(t, "news")
	require.Eventually(t, func() bool { _, ok := h.tuner.last(h.key("news")); return ok }, 10*time.Second, 50*time.Millisecond)
	h.tuner.set(h.key("news"), snapshot())
	h.wake("news")
	require.Eventually(t, func() bool {
		p := h.get(t, "news")
		return p.Status.Lineup != nil && p.Status.Lineup.Hash == "lh"
	}, 10*time.Second, 50*time.Millisecond)
}

func assertSteadyStatus(t *testing.T, st clustarrv1.IPTVProviderStatus) {
	t.Helper()
	require.NotNil(t, st.Playlist)
	assert.Equal(t, int32(120), st.Playlist.Entries)
	assert.Equal(t, int32(7), st.Playlist.Groups)
	assert.Equal(t, int32(40), st.Playlist.Candidates)
	assert.Equal(t, "ph", st.Playlist.Hash)
	require.NotNil(t, st.Playlist.FetchedAt)
	assert.True(t, st.Playlist.FetchedAt.Equal(&metav1.Time{Time: time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)}))
	require.Len(t, st.Guides, 1)
	assert.Equal(t, clustarrv1.GuideStatus{Name: "main", Channels: 300, Programmes: 9000, FetchedAt: st.Guides[0].FetchedAt}, st.Guides[0])
	require.NotNil(t, st.Lineup)
	assert.Equal(t, clustarrv1.LineupStatus{Active: 12, Unmapped: 2, Missing: 1, Hash: "lh"}, *st.Lineup)
	assert.Equal(t, "gh", st.GuideHash)
	require.NotNil(t, st.Tuners)
	assert.Equal(t, clustarrv1.TunerStatus{Total: 2, InUse: 1}, *st.Tuners)
	assert.NotEmpty(t, st.Address)
	assert.NotEmpty(t, st.DeviceID)
}

func TestProviderStatusComesFromTheSnapshot(t *testing.T) {
	h := setup(t)
	steady(t, h)
	p := h.get(t, "news")
	assertSteadyStatus(t, p.Status)
	for _, ct := range []string{clustarrv1.ConditionValid, clustarrv1.ConditionPlaylistFetched, clustarrv1.ConditionGuidesFetched, clustarrv1.ConditionReady} {
		c := meta.FindStatusCondition(p.Status.Conditions, ct)
		require.NotNil(t, c, ct)
		assert.Equal(t, metav1.ConditionTrue, c.Status, ct)
	}
}

func TestAnEarlyReturnKeepsTheStatus(t *testing.T) {
	h := setup(t)
	steady(t, h)
	h.tuner.set(h.key("news"), nil) // the tuner no longer answers for it
	require.NoError(t, h.c.Delete(h.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "news-livetv", Namespace: h.ns}}))
	h.wake("news")
	var p *clustarrv1.IPTVProvider
	require.Eventually(t, func() bool {
		p = h.get(t, "news")
		c := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionValid)
		return c != nil && c.Status == metav1.ConditionFalse
	}, 10*time.Second, 50*time.Millisecond)
	assertSteadyStatus(t, p.Status)
	for _, mf := range p.ManagedFields {
		if mf.Subresource == "status" {
			assert.Equal(t, k8s.ManagerLiveTV.String(), mf.Manager, "status has one manager")
		}
	}
}

func TestChannelsReachTheTunerThroughTheIndex(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	h.secret(t, "other-livetv", playlistURL)
	h.provider(t, "news")
	h.provider(t, "other")
	mk := func(name, provider, key, number string, epg *clustarrv1.ChannelEPG) {
		require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: provider, Key: key, Number: number, Active: true, EPG: epg, TimeshiftMinutes: 30},
		}))
	}
	mk("news-a", "news", "t:a", "1001", &clustarrv1.ChannelEPG{Guide: "main", ChannelID: "a.uk"})
	mk("news-b", "news", "t:b", "1002", &clustarrv1.ChannelEPG{Dummy: true, DummyMinutes: ptr.To(int32(120))})
	mk("other-a", "other", "t:a", "1001", nil)
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return len(c.Channels) == 2 }, 10*time.Second, 50*time.Millisecond)
	c, _ := h.tuner.last(h.key("news"))
	byObject := map[string]tuner.ChannelConfig{}
	for _, ch := range c.Channels {
		byObject[ch.Object] = ch
	}
	assert.Equal(t, "t:a", byObject["news-a"].Key)
	assert.Equal(t, "1001", byObject["news-a"].Number)
	assert.True(t, byObject["news-a"].Active)
	require.NotNil(t, byObject["news-a"].Map)
	assert.Equal(t, "a.uk", byObject["news-a"].Map.ChannelID)
	assert.Equal(t, 30, byObject["news-a"].TimeshiftMinutes)
	assert.Nil(t, byObject["news-b"].Map)
	assert.Equal(t, 120, byObject["news-b"].DummyMinutes)
	mk("news-c", "news", "t:c", "1003", nil)
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return len(c.Channels) == 3 }, 10*time.Second, 50*time.Millisecond)
}

func TestADisabledProviderIsRemovedFromTheTunerButKeepsItsService(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	h.provider(t, "news", func(p *clustarrv1.IPTVProvider) { p.Spec.Enabled = ptr.To(false) })
	require.Eventually(t, func() bool { return h.tuner.removals(h.key("news")) > 0 }, 10*time.Second, 50*time.Millisecond)
	_, err := h.service("news")
	assert.NoError(t, err)
	require.Eventually(t, func() bool {
		c := meta.FindStatusCondition(h.get(t, "news").Status.Conditions, clustarrv1.ConditionReady)
		return c != nil && c.Reason == clustarrv1.ReasonDisabled && c.Status == metav1.ConditionFalse
	}, 10*time.Second, 50*time.Millisecond)
}

func TestARefreshAnnotationReachesTheTuner(t *testing.T) {
	h := setup(t)
	h.secret(t, "news-livetv", playlistURL)
	h.provider(t, "news", func(p *clustarrv1.IPTVProvider) {
		p.Annotations = map[string]string{iptvprovider.AnnotationRefresh: "2026-10-07T01:00:00Z"}
	})
	require.Eventually(t, func() bool { c, _ := h.tuner.last(h.key("news")); return c.Refresh == "2026-10-07T01:00:00Z" }, 10*time.Second, 50*time.Millisecond)
}
