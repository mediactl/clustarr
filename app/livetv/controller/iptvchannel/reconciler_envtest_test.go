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

package iptvchannel_test

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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"github.com/mediactl/clustarr/app/livetv/controller/iptvchannel"
	"github.com/mediactl/clustarr/app/livetv/tuner"
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

// snapshots is a settable tuner snapshot per provider.
type snapshots struct {
	mu    sync.Mutex
	snaps map[types.NamespacedName]tuner.Snapshot
}

func (s *snapshots) Snapshot(key types.NamespacedName) (tuner.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.snaps[key]
	return v, ok
}

func (s *snapshots) set(key types.NamespacedName, v tuner.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps[key] = v
}

// countingClient counts the status applies sent through it.
type countingClient struct {
	client.Client
	n *atomic.Int64
}

func (c countingClient) Status() client.SubResourceWriter { return countingWriter{c.Client.Status(), c.n} }

type countingWriter struct {
	client.SubResourceWriter
	n *atomic.Int64
}

func (w countingWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	w.n.Add(1)
	return w.SubResourceWriter.Apply(ctx, obj, opts...)
}

type harness struct {
	c      client.Client
	writes atomic.Int64 // status applies the reconciler sent
	snaps  *snapshots
	events chan event.GenericEvent
	ns     string
	ctx    context.Context
}

var nsSeq atomic.Int32

func setup(t *testing.T) *harness {
	t.Helper()
	if cfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	h := &harness{snaps: &snapshots{snaps: map[types.NamespacedName]tuner.Snapshot{}}, events: make(chan event.GenericEvent, 16), ns: fmt.Sprintf("media-%d", nsSeq.Add(1))}
	scheme := k8s.MustNewScheme()
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, direct.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}}))
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	// Count the status writes: an identical server-side apply leaves
	// resourceVersion alone, so only the requests show a wasted write.
	r := &iptvchannel.Reconciler{Client: countingClient{mgr.GetClient(), &h.writes}, Snapshots: h.snaps, Events: h.events}
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.c, h.ctx = direct, ctx
	return h
}

func (h *harness) provider(t *testing.T, name string) types.NamespacedName {
	require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns}, Spec: clustarrv1.IPTVProviderSpec{
		Tuners: 1, Playlist: clustarrv1.PlaylistSource{URL: "http://p.example/x.m3u"},
	}}))
	return types.NamespacedName{Namespace: h.ns, Name: name}
}

func (h *harness) channel(t *testing.T, name, provider, number string, active bool) {
	require.NoError(t, h.c.Create(h.ctx, &clustarrv1.IPTVChannel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: clustarrv1.IPTVChannelSpec{
			ProviderRef: provider, Key: "t:" + name, Number: number, Active: active,
			EPG: &clustarrv1.ChannelEPG{Guide: "main", ChannelID: name + ".uk"},
		},
	}))
}

func (h *harness) wake(key types.NamespacedName) {
	h.events <- event.GenericEvent{Object: &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}}
}

func (h *harness) get(t *testing.T, name string) *clustarrv1.IPTVChannel {
	var ch clustarrv1.IPTVChannel
	require.NoError(t, h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, &ch))
	return &ch
}

func reason(ch *clustarrv1.IPTVChannel) string {
	if c := meta.FindStatusCondition(ch.Status.Conditions, clustarrv1.ConditionReady); c != nil {
		return c.Reason
	}
	return ""
}

func (h *harness) until(t *testing.T, name, want string) *clustarrv1.IPTVChannel {
	t.Helper()
	var ch *clustarrv1.IPTVChannel
	require.Eventually(t, func() bool { ch = h.get(t, name); return reason(ch) == want }, 15*time.Second, 50*time.Millisecond, "%s reads %s", name, want)
	return ch
}

func TestChannelStatusReadsTheSnapshot(t *testing.T) {
	h := setup(t)
	key := h.provider(t, "news")
	for i, c := range []struct {
		name   string
		active bool
	}{{"a", true}, {"b", true}, {"c", true}, {"d", true}, {"e", false}} {
		h.channel(t, c.name, "news", fmt.Sprint(1001+i), c.active)
	}
	h.until(t, "a", "Pending")
	h.snaps.set(key, tuner.Snapshot{Channels: map[string]tuner.ChannelState{
		"a": {Guide: "main/a.uk"}, "b": {Missing: true}, "c": {Duplicate: true},
		"d": {GuideNotFound: true, Guide: "dummy/60"}, "e": {},
	}})
	h.wake(key)
	assert.Equal(t, "main/a.uk", h.until(t, "a", clustarrv1.ReasonActive).Status.Guide)
	h.until(t, "b", clustarrv1.ReasonMissing)
	h.until(t, "c", clustarrv1.ReasonDuplicateNumber)
	assert.Equal(t, "dummy/60", h.until(t, "d", clustarrv1.ReasonGuideNotFound).Status.Guide)
	h.until(t, "e", clustarrv1.ReasonInactive)
}

func TestAChannelGoneFromThePlaylistIsMissingThenActiveAgain(t *testing.T) {
	h := setup(t)
	key := h.provider(t, "news")
	h.channel(t, "bbc1", "news", "1001", true)
	h.snaps.set(key, tuner.Snapshot{Channels: map[string]tuner.ChannelState{"bbc1": {Guide: "main/bbc1.uk"}}})
	h.wake(key)
	before := h.until(t, "bbc1", clustarrv1.ReasonActive)

	h.snaps.set(key, tuner.Snapshot{Channels: map[string]tuner.ChannelState{"bbc1": {Missing: true}}})
	h.wake(key)
	missing := h.until(t, "bbc1", clustarrv1.ReasonMissing)
	assert.Equal(t, before.Spec, missing.Spec, "the mapping is untouched; nothing is deleted")

	h.snaps.set(key, tuner.Snapshot{Channels: map[string]tuner.ChannelState{"bbc1": {Guide: "main/bbc1.uk"}}})
	h.wake(key)
	back := h.until(t, "bbc1", clustarrv1.ReasonActive)
	assert.Equal(t, before.Spec, back.Spec)
	assert.Equal(t, "main/bbc1.uk", back.Status.Guide)
}

func TestAChannelOfAnUnknownProviderIsNotReady(t *testing.T) {
	h := setup(t)
	h.channel(t, "lost", "nobody", "1", true)
	ch := h.until(t, "lost", clustarrv1.ReasonProviderNotFound)
	assert.Equal(t, metav1.ConditionFalse, meta.FindStatusCondition(ch.Status.Conditions, clustarrv1.ConditionReady).Status)
}

func TestAnUnchangedSnapshotWritesNothingAcross3000Channels(t *testing.T) {
	h := setup(t)
	key := h.provider(t, "news")
	states := map[string]tuner.ChannelState{}
	const n = 3000
	for i := range n {
		name := fmt.Sprintf("c%04d", i)
		states[name] = tuner.ChannelState{Guide: "main/" + name}
	}
	h.snaps.set(key, tuner.Snapshot{Channels: states})
	for i := range n {
		h.channel(t, fmt.Sprintf("c%04d", i), "news", fmt.Sprint(1000+i), true)
	}
	versions := func() map[string]string {
		var l clustarrv1.IPTVChannelList
		require.NoError(t, h.c.List(h.ctx, &l, client.InNamespace(h.ns)))
		out := map[string]string{}
		for _, ch := range l.Items {
			if reason(&ch) == clustarrv1.ReasonActive {
				out[ch.Name] = ch.ResourceVersion
			}
		}
		return out
	}
	require.Eventually(t, func() bool { return len(versions()) == n }, 120*time.Second, 500*time.Millisecond, "every channel Active")
	before := versions()

	writes := h.writes.Load()
	h.wake(key) // a refresh whose snapshot did not change
	time.Sleep(3 * time.Second)
	assert.Equal(t, before, versions(), "no channel's status changed")
	assert.Equal(t, writes, h.writes.Load(), "and none was sent: 3,000 identical applies per refresh")

	var ch clustarrv1.IPTVChannel
	require.NoError(t, h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: "c0000"}, &ch))
	var managers []string
	for _, mf := range ch.ManagedFields {
		if mf.Subresource == "status" {
			managers = append(managers, mf.Manager)
		}
	}
	assert.Equal(t, []string{string(k8s.ManagerLiveTVChannel)}, managers, "one manager writes a channel's status")
}
