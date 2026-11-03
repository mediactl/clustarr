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

package rowcache

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	toolscache "k8s.io/client-go/tools/cache"
	fcache "k8s.io/client-go/tools/cache/testing"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

const seriesField = "spec.seriesRef"

func episode(ns, series string, n int) *catalogv1alpha1.Episode {
	return &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-s01e%03d", series, n),
			Namespace: ns,
			Labels:    map[string]string{"series": series, "parity": strconv.Itoa(n % 2)},
		},
		Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: series, SeasonNumber: 1, EpisodeNumber: int32(n)}, //nolint:gosec // a test fixture
		Status: catalogv1alpha1.EpisodeStatus{Conditions: []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reconciled", Message: "reconciled",
			LastTransitionTime: metav1.NewTime(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)),
		}}},
	}
}

// seriesIndex is the series index as controller-runtime's IndexField
// registers one: a namespaced key and an all-namespaces key per value.
func seriesIndex(obj any) ([]string, error) {
	e, ok := obj.(*catalogv1alpha1.Episode)
	if !ok {
		return nil, fmt.Errorf("seriesIndex: %T is not an Episode", obj)
	}
	return []string{keyToNamespacedKey("", e.Spec.SeriesRef), keyToNamespacedKey(e.Namespace, e.Spec.SeriesRef)}, nil
}

type event struct {
	op       string
	obj, old any
	initial  bool
}

// recorder is a handler that keeps every notification it was given.
type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) OnAdd(obj any, initial bool) {
	r.record(event{op: "add", obj: obj, initial: initial})
}

func (r *recorder) OnUpdate(old, obj any) { r.record(event{op: "update", obj: obj, old: old}) }
func (r *recorder) OnDelete(obj any)      { r.record(event{op: "delete", obj: obj}) }

func (r *recorder) record(e event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) of(op string) []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []event
	for _, e := range r.events {
		if e.op == op {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) count(op string) int { return len(r.of(op)) }

// fakeCache stands in for controller-runtime's cache: it hands out the
// informers it was given and counts the reads delegated to it.
type fakeCache struct {
	cache.Cache
	informers   map[reflect.Type]cache.Informer
	gets, lists int
}

func (f *fakeCache) GetInformer(_ context.Context, obj client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	inf, ok := f.informers[reflect.TypeOf(obj)]
	if !ok {
		return nil, fmt.Errorf("fakeCache: no informer for %T", obj)
	}
	return inf, nil
}

func (f *fakeCache) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	f.gets++
	return nil
}

func (f *fakeCache) List(context.Context, client.ObjectList, ...client.ListOption) error {
	f.lists++
	return nil
}

type harness struct {
	t     *testing.T
	src   *fcache.FakeControllerSource
	inf   *Informer
	k     *kind
	cache *Cache
	inner *fakeCache
}

func newHarness(t *testing.T, episodes ...*catalogv1alpha1.Episode) *harness {
	t.Helper()
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	scheme := k8s.MustNewScheme()
	gvk, err := apiutil.GVKForObject(&catalogv1alpha1.Episode{}, scheme)
	require.NoError(t, err)
	typ := reflect.TypeFor[catalogv1alpha1.Episode]()
	c, err := newCodec(gvk.Kind, gvk, typ)
	require.NoError(t, err)
	k := &kind{gvk: gvk, typ: typ, obj: &catalogv1alpha1.Episode{}, codec: c}
	src := fcache.NewFakeControllerSource()
	for _, e := range episodes {
		src.Add(e)
	}
	inf := newInformer(src, &catalogv1alpha1.Episode{}, k, 0, toolscache.Indexers{toolscache.NamespaceIndex: toolscache.MetaNamespaceIndexFunc})
	inf.dictionaryOnSync = false
	require.NoError(t, inf.SetTransform(k8s.CacheTransform))
	require.NoError(t, inf.AddIndexers(toolscache.Indexers{fieldIndexName(seriesField): seriesIndex}))
	inner := &fakeCache{informers: map[reflect.Type]cache.Informer{reflect.TypeOf(&catalogv1alpha1.Episode{}): inf}}
	rc := &Cache{
		Cache: inner,
		kinds: map[reflect.Type]*kind{reflect.TypeOf(&catalogv1alpha1.Episode{}): k},
		lists: map[reflect.Type]*kind{reflect.TypeOf(&catalogv1alpha1.EpisodeList{}): k},
	}
	return &harness{t: t, src: src, inf: inf, k: k, cache: rc, inner: inner}
}

func (h *harness) run() context.Context {
	h.t.Helper()
	ctx, cancel := context.WithCancel(h.t.Context())
	h.t.Cleanup(cancel)
	go h.inf.RunWithContext(ctx)
	require.Eventually(h.t, h.inf.HasSynced, 10*time.Second, 10*time.Millisecond, "the informer syncs")
	return ctx
}

func (h *harness) get(ctx context.Context, e *catalogv1alpha1.Episode) (*catalogv1alpha1.Episode, error) {
	var got catalogv1alpha1.Episode
	err := h.cache.Get(ctx, client.ObjectKeyFromObject(e), &got)
	return &got, err
}

func (h *harness) rows() []*Row {
	var out []*Row
	for _, item := range h.inf.indexer.List() {
		r, ok := item.(*Row)
		require.True(h.t, ok, "the store holds a %T, not a row", item)
		out = append(out, r)
	}
	return out
}

func (h *harness) bytes() int64 {
	h.inf.mu.Lock()
	defer h.inf.mu.Unlock()
	return h.inf.bytes
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

func episodes(ns, series string, n int) []*catalogv1alpha1.Episode {
	out := make([]*catalogv1alpha1.Episode, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, episode(ns, series, i))
	}
	return out
}

func assertSameEpisode(t *testing.T, want, got *catalogv1alpha1.Episode) {
	t.Helper()
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.Namespace, got.Namespace)
	assert.Equal(t, want.Labels, got.Labels)
	assert.Equal(t, want.Spec, got.Spec)
	// metav1.Time comes back from JSON in the local zone (CLAUDE.md): the
	// instant is what must survive.
	status := got.Status.DeepCopy()
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.NewTime(status.Conditions[i].LastTransitionTime.UTC())
	}
	assert.Equal(t, want.Status, *status)
	assert.Equal(t, "catalog.clustarr.io/v1alpha1", got.APIVersion, "the GroupVersionKind is set, as the informer cache sets it")
	assert.Equal(t, "Episode", got.Kind)
}

// The store holds rows and nothing else; every handler, Get and List gets
// a fresh object decoded from one.
func TestTheStoreHoldsRowsAndEveryReaderGetsAnObject(t *testing.T) {
	a, b := episodes("media", "a", 100), episodes("media", "b", 100)
	h := newHarness(t, append(a, b...)...)
	early := &recorder{}
	reg, err := h.inf.AddEventHandler(early)
	require.NoError(t, err)
	ctx := h.run()

	require.Eventually(t, func() bool { return early.count("add") == 200 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, reg.HasSynced, 5*time.Second, 10*time.Millisecond, "a handler registered before the start is synced once it has the initial list")
	for _, e := range early.of("add") {
		assert.IsType(t, &catalogv1alpha1.Episode{}, e.obj)
		assert.True(t, e.initial, "the initial list is delivered as such")
	}

	rows := h.rows()
	require.Len(t, rows, 200)
	for _, r := range rows {
		assert.NotEmpty(t, r.body)
		assert.Less(t, len(r.body), r.size, "a row is smaller than its JSON")
		assert.Zero(t, r.dict, "no dictionary yet")
		vals, ok := r.indexValues(fieldIndexName(seriesField))
		assert.True(t, ok, "the series index was computed while the object was decoded")
		assert.Len(t, vals, 2)
	}

	got, err := h.get(ctx, a[3])
	require.NoError(t, err)
	assertSameEpisode(t, a[3], got)

	// An update: the handler sees both states, a Get the new one.
	changed := a[3].DeepCopy()
	changed.Spec.SeasonNumber = 2
	h.src.Modify(changed)
	require.Eventually(t, func() bool { return early.count("update") == 1 }, 5*time.Second, 10*time.Millisecond)
	up := early.of("update")[0]
	assert.Equal(t, int32(1), up.old.(*catalogv1alpha1.Episode).Spec.SeasonNumber)
	assert.Equal(t, int32(2), up.obj.(*catalogv1alpha1.Episode).Spec.SeasonNumber)
	got, err = h.get(ctx, a[3])
	require.NoError(t, err)
	assert.Equal(t, int32(2), got.Spec.SeasonNumber)

	// A delete: the handler gets the object, a Get the apiserver's NotFound.
	h.src.Delete(changed)
	require.Eventually(t, func() bool { return early.count("delete") == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.IsType(t, &catalogv1alpha1.Episode{}, early.of("delete")[0].obj)
	_, err = h.get(ctx, a[3])
	assert.True(t, apierrors.IsNotFound(err), "got %v", err)
	assert.Len(t, h.rows(), 199)

	// A late handler is told of everything in the store, as the initial
	// list, then of the sync.
	late := &recorder{}
	reg2, err := h.inf.AddEventHandler(late)
	require.NoError(t, err)
	require.Eventually(t, reg2.HasSynced, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 199, late.count("add"))
	for _, e := range late.of("add") {
		assert.True(t, e.initial)
	}
	require.NoError(t, h.inf.RemoveEventHandler(reg2))
}

// A tombstone and a resync go through the same decode as every event.
func TestTombstonesAndResyncsCarryObjects(t *testing.T) {
	eps := episodes("media", "a", 10)
	h := newHarness(t, eps...)
	rec := &recorder{}
	_, err := h.inf.AddEventHandler(rec)
	require.NoError(t, err)
	h.run()
	require.Eventually(t, func() bool { return rec.count("add") == 10 }, 5*time.Second, 10*time.Millisecond)

	item, ok, err := h.inf.indexer.GetByKey("media/" + eps[2].Name)
	require.NoError(t, err)
	require.True(t, ok)
	stored := item.(*Row)

	require.NoError(t, h.inf.process(toolscache.Deltas{{Type: toolscache.Sync, Object: stored}}, false))
	require.Eventually(t, func() bool { return rec.count("update") == 1 }, 5*time.Second, 10*time.Millisecond)
	up := rec.of("update")[0]
	assert.Same(t, up.old, up.obj, "a resync decodes the row once and hands it as both states")
	assertSameEpisode(t, eps[2], up.obj.(*catalogv1alpha1.Episode))

	tomb := toolscache.DeletedFinalStateUnknown{Key: "media/" + eps[2].Name, Obj: stored}
	require.NoError(t, h.inf.process(toolscache.Deltas{{Type: toolscache.Deleted, Object: tomb}}, false))
	require.Eventually(t, func() bool { return rec.count("delete") == 1 }, 5*time.Second, 10*time.Millisecond)
	del, ok := rec.of("delete")[0].obj.(toolscache.DeletedFinalStateUnknown)
	require.True(t, ok, "a tombstone stays a tombstone")
	assert.Equal(t, tomb.Key, del.Key)
	assertSameEpisode(t, eps[2], del.Obj.(*catalogv1alpha1.Episode))
	assert.Len(t, h.rows(), 9)
}

// List narrows on the namespace, the label selector and the field index
// before it decodes a row, and only the rows it answers are decoded.
func TestListNarrowsBeforeItDecodes(t *testing.T) {
	media := append(episodes("media", "a", 100), episodes("media", "b", 100)...)
	other := episodes("other", "a", 50)
	h := newHarness(t, append(media, other...)...)
	ctx := h.run()

	list := func(opts ...client.ListOption) []catalogv1alpha1.Episode {
		t.Helper()
		var l catalogv1alpha1.EpisodeList
		require.NoError(t, h.cache.List(ctx, &l, opts...))
		return l.Items
	}
	reads := func() float64 {
		return counterValue(t, metrics.RowCacheDecodesTotal.WithLabelValues("Episode", reasonRead))
	}

	assert.Len(t, list(), 250)
	before := reads()
	assert.Len(t, list(client.InNamespace("other")), 50)
	assert.InDelta(t, 50, reads()-before, 0, "a namespaced List decodes the namespace's rows and no other")
	assert.Len(t, list(client.InNamespace("media"), client.MatchingLabels{"parity": "0"}), 100)
	assert.Len(t, list(client.InNamespace("media"), client.MatchingFields{seriesField: "a"}), 100)
	assert.Len(t, list(client.MatchingFields{seriesField: "a"}), 150, "the all-namespaces index key")
	assert.Len(t, list(client.MatchingFields{seriesField: "a"}, client.MatchingLabels{"parity": "1"}), 75)
	assert.Len(t, list(client.InNamespace("media"), client.Limit(10)), 10)
	for _, e := range list(client.InNamespace("other")) {
		assert.Equal(t, "other", e.Namespace)
		assert.Equal(t, "Episode", e.Kind)
	}

	var l catalogv1alpha1.EpisodeList
	assert.Error(t, h.cache.List(ctx, &l, client.Continue("x")))
	assert.Error(t, h.cache.List(ctx, &l, &client.ListOptions{FieldSelector: fields.ParseSelectorOrDie("spec.seriesRef!=a")}))

	// A kind not held as rows is the cache's own.
	require.NoError(t, h.cache.Get(ctx, types.NamespacedName{Namespace: "media", Name: "x"}, &corev1.ConfigMap{}))
	require.NoError(t, h.cache.List(ctx, &corev1.ConfigMapList{}))
	assert.Equal(t, 1, h.inner.gets)
	assert.Equal(t, 1, h.inner.lists)
}

// A dictionary built from the rows shrinks them in place; what was stored
// before it and what arrives after it read the same.
func TestADictionaryShrinksTheRowsInPlace(t *testing.T) {
	eps := episodes("media", "a", 300)
	h := newHarness(t, eps...)
	ctx := h.run()
	before := h.bytes()
	require.Positive(t, before)

	require.NoError(t, h.inf.buildDictionary(ctx))
	after := h.bytes()
	assert.Less(t, after, before*7/10, "the dictionary takes at least 30%% off the rows (%d -> %d)", before, after)
	for _, r := range h.rows() {
		assert.Equal(t, uint32(dictionaryID), r.dict, "%s was compressed again with the dictionary", r.key())
	}
	got, err := h.get(ctx, eps[7])
	require.NoError(t, err)
	assertSameEpisode(t, eps[7], got)

	fresh := episode("media", "a", 301)
	h.src.Add(fresh)
	require.Eventually(t, func() bool {
		item, ok, _ := h.inf.indexer.GetByKey("media/" + fresh.Name)
		return ok && item.(*Row).dict == dictionaryID
	}, 5*time.Second, 10*time.Millisecond, "a row stored after the dictionary uses it")
	got, err = h.get(ctx, fresh)
	require.NoError(t, err)
	assertSameEpisode(t, fresh, got)
}

// Starting the informer twice is a no-op, and a handler cannot be added
// once it has stopped.
func TestLifecycle(t *testing.T) {
	h := newHarness(t, episodes("media", "a", 3)...)
	assert.False(t, h.inf.IsStopped())
	assert.Empty(t, h.inf.LastSyncResourceVersion())
	ctx, cancel := context.WithCancel(t.Context())
	go h.inf.RunWithContext(ctx)
	require.Eventually(t, h.inf.HasSynced, 10*time.Second, 10*time.Millisecond)
	assert.Error(t, h.inf.SetTransform(nil), "the transform is fixed once started")
	assert.NotEmpty(t, h.inf.LastSyncResourceVersion())
	h.inf.RunWithContext(ctx) // a second start returns at once
	cancel()
	require.Eventually(t, h.inf.IsStopped, 5*time.Second, 10*time.Millisecond)
	_, err := h.inf.AddEventHandler(&recorder{})
	assert.Error(t, err)
}
