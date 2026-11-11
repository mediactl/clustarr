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

package lineup_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/controller/lineup"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
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

// fieldSet is one managedFields entry's FieldsV1 as a tree.
type fieldSet map[string]any

// keys are the set's member names at path (each "f:<field>"), "." left out.
func (s fieldSet) keys(path ...string) []string {
	m := map[string]any(s)
	for _, p := range path {
		next, ok := m[p].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	var out []string
	for k := range m {
		if k != "." {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// owned is manager's managedFields entry for subresource ("" the main
// resource) and operation; nil when it has none.
func owned(t *testing.T, ch *clustarrv1.IPTVChannel, manager, subresource string, op metav1.ManagedFieldsOperationType) fieldSet {
	t.Helper()
	for _, mf := range ch.ManagedFields {
		if mf.Manager != manager || mf.Subresource != subresource || mf.Operation != op {
			continue
		}
		require.NotNil(t, mf.FieldsV1)
		var s fieldSet
		require.NoError(t, json.Unmarshal(mf.FieldsV1.GetRawBytes(), &s))
		return s
	}
	return nil
}

func lineupEntry(name string) m3u.Entry {
	return m3u.Entry{Key: m3u.NameKey("NEWS", name), Name: name, Group: "NEWS"}
}

// TestTheLineupManagerOwnsOnlyItsFields: on a real apiserver, after a
// lineup pass, a ui merge patch of spec.name and a second pass, the lineup's
// manager owns exactly spec.providerRef, key, number and active (and the
// origin label of the channel it created, and status.lineup), and the ui
// keeps spec.name. A channel the ui made keeps the ui's label, and an
// absent channel's spec.active: false reaches the object (the fake client
// drops it; see applyKeepingFalse).
func TestTheLineupManagerOwnsOnlyItsFields(t *testing.T) {
	if cfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	const ns, name = "lineup-owns", "argon"
	ctx, cancel := context.WithCancel(context.Background())
	scheme := k8s.MustNewScheme()
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, index.Register(ctx, mgr))
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	cached := mgr.GetClient()

	require.NoError(t, direct.Create(ctx, &clustarrv1.IPTVProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: clustarrv1.IPTVProviderSpec{
			Server: "p.example", CredentialsRef: corev1.LocalObjectReference{Name: name + "-credentials"},
			Lineup: &clustarrv1.LineupSpec{Enabled: true, Categories: []clustarrv1.LineupCategory{
				{Name: "News", Base: ptr.To(int32(1000)), Names: []string{"^CNN", "NEWS$"}},
			}},
		},
	}))
	cnn, bbc, fox := lineupEntry("CNN"), lineupEntry("BBC NEWS"), lineupEntry("FOX NEWS")
	objectName := func(e m3u.Entry) string { return iptv.ChannelObjectName(name, e.Key) }
	// BBC's channel is the ui's, as actions.AddChannel makes one.
	require.NoError(t, direct.Create(ctx, &clustarrv1.IPTVChannel{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: objectName(bbc), Labels: map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginUI}},
		Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: name, Key: bbc.Key, Number: "5", Active: true},
	}, client.FieldOwner(k8s.ManagerUI.String())))
	cachedCount := func(n int) func() bool {
		return func() bool {
			chans, err := index.ChannelsOf(ctx, cached, ns, name)
			return err == nil && len(chans) == n
		}
	}
	require.Eventually(t, cachedCount(1), 10*time.Second, 20*time.Millisecond)

	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	entries := []m3u.Entry{cnn, bbc, fox}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	r := &lineup.Reconciler{
		Client: cached, Store: bus.ObjectStore(events.BucketLiveTV), Now: func() time.Time { return now },
		Entries: func(types.NamespacedName) ([]m3u.Entry, bool) { return slices.Clone(entries), true },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Eventually(t, cachedCount(3), 10*time.Second, 20*time.Millisecond)

	// A person names CNN's channel; FOX leaves the playlist.
	require.NoError(t, direct.Patch(ctx, &clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: objectName(cnn)}},
		client.RawPatch(types.MergePatchType, []byte(`{"spec":{"name":"AAA Cable"}}`)), client.FieldOwner(k8s.ManagerUI.String())))
	require.Eventually(t, func() bool {
		var ch clustarrv1.IPTVChannel
		return cached.Get(ctx, client.ObjectKey{Namespace: ns, Name: objectName(cnn)}, &ch) == nil && ch.Spec.Name == "AAA Cable"
	}, 10*time.Second, 20*time.Millisecond)
	entries = []m3u.Entry{cnn, bbc}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	read := func(e m3u.Entry) *clustarrv1.IPTVChannel {
		var ch clustarrv1.IPTVChannel
		require.NoError(t, direct.Get(ctx, client.ObjectKey{Namespace: ns, Name: objectName(e)}, &ch))
		return &ch
	}
	lineupManager := k8s.ManagerLiveTVLineup.String()
	specFields := []string{"f:active", "f:key", "f:number", "f:providerRef"}

	// CNN: the lineup's own channel, renamed by a person.
	ch := read(cnn)
	assert.Equal(t, "AAA Cable", ch.Spec.Name)
	assert.Equal(t, "1001", ch.Spec.Number, "sorted by the person's name, ahead of BBC NEWS")
	assert.True(t, ch.Spec.Active)
	assert.Equal(t, clustarrv1.OriginLineup, ch.Labels[clustarrv1.LabelOrigin])
	main := owned(t, ch, lineupManager, "", metav1.ManagedFieldsOperationApply)
	require.NotNil(t, main, "%v", ch.ManagedFields)
	assert.Equal(t, []string{"f:metadata", "f:spec"}, main.keys())
	assert.Equal(t, specFields, main.keys("f:spec"))
	assert.Equal(t, []string{"f:labels"}, main.keys("f:metadata"))
	assert.Equal(t, []string{"f:" + clustarrv1.LabelOrigin}, main.keys("f:metadata", "f:labels"))
	status := owned(t, ch, lineupManager, "status", metav1.ManagedFieldsOperationApply)
	require.NotNil(t, status, "%v", ch.ManagedFields)
	assert.Equal(t, []string{"f:status"}, status.keys())
	assert.Equal(t, []string{"f:lineup"}, status.keys("f:status"))
	ui := owned(t, ch, k8s.ManagerUI.String(), "", metav1.ManagedFieldsOperationUpdate)
	require.NotNil(t, ui, "%v", ch.ManagedFields)
	assert.Equal(t, []string{"f:name"}, ui.keys("f:spec"))

	// BBC: the ui's channel, renumbered; its label stays the ui's.
	ch = read(bbc)
	assert.Equal(t, "1002", ch.Spec.Number)
	assert.Equal(t, clustarrv1.OriginUI, ch.Labels[clustarrv1.LabelOrigin])
	main = owned(t, ch, lineupManager, "", metav1.ManagedFieldsOperationApply)
	require.NotNil(t, main, "%v", ch.ManagedFields)
	assert.Equal(t, []string{"f:spec"}, main.keys(), "no label of the ui's is taken")
	assert.Equal(t, specFields, main.keys("f:spec"))

	// FOX: absent, so deactivated -- a false the apiserver keeps.
	ch = read(fox)
	assert.False(t, ch.Spec.Active)
	assert.Equal(t, "1003", ch.Spec.Number, "its number as it was")
	require.NotNil(t, ch.Status.Lineup)
	require.NotNil(t, ch.Status.Lineup.MissingSince)
	assert.Equal(t, now, ch.Status.Lineup.MissingSince.UTC())
	main = owned(t, ch, lineupManager, "", metav1.ManagedFieldsOperationApply)
	require.NotNil(t, main, "%v", ch.ManagedFields)
	assert.Equal(t, specFields, main.keys("f:spec"))
}
